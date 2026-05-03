package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type EnqueueReq struct {
	GUID         string            `json:"guid,omitempty"`
	Script       string            `json:"script"`
	Env          map[string]string `json:"env,omitempty"`
	Descr        string            `json:"descr,omitempty"`
	Root         string            `json:"root,omitempty"`
	Slots        int               `json:"slots,omitempty"`
	RetryOnError int               `json:"retry_on_error,omitempty"`
}

type EnqueueResp struct {
	GUID string `json:"guid"`
}

type StateResp struct {
	GUID  string `json:"guid"`
	State string `json:"state"`
}

type QueuedResp struct {
	GUID   string `json:"guid"`
	Queued bool   `json:"queued"`
}

type EndpointInfo struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	User string `json:"user"`
	Path string `json:"path"`
}

type EndpointsResp struct {
	Endpoints []EndpointInfo `json:"endpoints"`
}

type TaskListItem struct {
	GUID           string            `json:"guid"`
	Env            map[string]string `json:"env,omitempty"`
	Descr          string            `json:"descr,omitempty"`
	Slots          int               `json:"slots,omitempty"`
	EnqueuedAt     string            `json:"enqueued_at,omitempty"`
	CreateRevision int64             `json:"create_revision"`
}

type TaskListResp struct {
	Tasks []TaskListItem `json:"tasks"`
}

type OutputResp struct {
	Result    json.RawMessage `json:"result"`
	StdoutB64 string          `json:"stdout_b64"`
	StderrB64 string          `json:"stderr_b64"`
}

func controlMain(args []string) {
	fs := flag.NewFlagSet("control", flag.ExitOnError)
	configPath := fs.String("config", "", "path to JSON config (required)")
	Throw(fs.Parse(args))

	if *configPath == "" {
		ThrowFmt("control: --config is required")
	}

	cfg := LoadConfig(*configPath)

	if cfg.Control.Listen == "" {
		ThrowFmt("control: control.listen is required in config")
	}

	if len(cfg.Etcd.Endpoints) == 0 {
		ThrowFmt("control: etcd.endpoints is required")
	}

	if cfg.S3.Bucket == "" {
		ThrowFmt("control: s3.bucket is required")
	}

	cli := newEtcdClient(cfg.Etcd)
	defer cli.Close()

	s3cli := newS3Client(cfg.S3)

	endpoints := make([]EndpointInfo, len(cfg.Endpoints))

	for i, ep := range cfg.Endpoints {
		endpoints[i] = EndpointInfo{Host: ep.Host, Port: ep.Port, User: ep.User, Path: ep.Path}
	}

	srv := &controlServer{etcd: cli, s3: s3cli, bucket: cfg.S3.Bucket, endpoints: endpoints, maxHostSlots: maxHostSlots(cfg.Endpoints)}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tasks", srv.handleTasks)
	mux.HandleFunc("/v1/tasks/", srv.handleTaskByID)
	mux.HandleFunc("/v1/endpoints", srv.handleEndpoints)

	server := &http.Server{
		Addr:    cfg.Control.Listen,
		Handler: mux,
	}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

		sig := <-sigs
		fmt.Fprintln(os.Stderr, "control: received signal:", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = server.Shutdown(ctx)
	}()

	fmt.Fprintln(os.Stderr, "control: listening on", cfg.Control.Listen)

	err := server.ListenAndServe()

	if err != nil && err != http.ErrServerClosed {
		Throw(err)
	}
}

type controlServer struct {
	etcd         *clientv3.Client
	s3           *s3.Client
	bucket       string
	endpoints    []EndpointInfo
	maxHostSlots int
}

// firstLine returns the first non-empty line of s stripped. Used as a
// human-readable fallback for Task.Descr when ignite didn't pass one.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)

		if t != "" {
			return t
		}
	}

	return ""
}

func maxHostSlots(eps []Endpoint) int {
	byHost := map[string]int{}

	for _, ep := range eps {
		byHost[ep.Host]++
	}

	max := 0

	for _, n := range byHost {
		if n > max {
			max = n
		}
	}

	return max
}

func (s *controlServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	exc := Try(func() {
		switch r.Method {
		case http.MethodPost:
			s.enqueue(w, r)
		case http.MethodGet:
			s.listTasks(w, r)
		default:
			ThrowHTTP(http.StatusMethodNotAllowed, "method not allowed")
		}
	})

	exc.Catch(func(e *Exception) {
		sendHTTPException(w, e)
	})
}

func (s *controlServer) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	exc := Try(func() {
		if r.Method != http.MethodGet {
			ThrowHTTP(http.StatusMethodNotAllowed, "method not allowed")
		}

		httpJSON(w, http.StatusOK, EndpointsResp{Endpoints: s.endpoints})
	})

	exc.Catch(func(e *Exception) {
		sendHTTPException(w, e)
	})
}

func (s *controlServer) listTasks(w http.ResponseWriter, r *http.Request) {
	items := queueList(r.Context(), s.etcd)
	out := make([]TaskListItem, len(items))

	for i, it := range items {
		out[i] = TaskListItem{
			GUID:           it.Task.GUID,
			Env:            it.Task.Env,
			Descr:          it.Task.Descr,
			Slots:          it.Task.Slots,
			EnqueuedAt:     it.Task.EnqueuedAt,
			CreateRevision: it.CreateRevision,
		}
	}

	httpJSON(w, http.StatusOK, TaskListResp{Tasks: out})
}

func (s *controlServer) enqueue(w http.ResponseWriter, r *http.Request) {
	body := Throw2(io.ReadAll(r.Body))

	var req EnqueueReq
	Throw(json.Unmarshal(body, &req))

	if req.Script == "" {
		ThrowHTTP(http.StatusBadRequest, "script is required")
	}

	guid := req.GUID

	if guid == "" {
		guid = newGUID()
	}

	descr := req.Descr

	if descr == "" {
		descr = firstLine(req.Script)
	}

	slots := req.Slots

	if slots <= 0 {
		slots = 1
	}

	if slots > s.maxHostSlots {
		ThrowHTTP(http.StatusBadRequest, "unschedulable: slots=%d > max host capacity=%d", slots, s.maxHostSlots)
	}

	task := Task{
		GUID:         guid,
		Script:       req.Script,
		Env:          req.Env,
		Descr:        descr,
		Root:         req.Root,
		Slots:        slots,
		EnqueuedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		RetryOnError: req.RetryOnError,
	}
	payload := Throw2(json.Marshal(task))
	key := queueKey(guid)

	resp := Throw2(s.etcd.Txn(r.Context()).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(payload))).
		Commit())

	if !resp.Succeeded {
		ThrowHTTP(http.StatusConflict, "task with guid %q already exists", guid)
	}

	httpJSON(w, http.StatusOK, EnqueueResp{GUID: guid})
}

func (s *controlServer) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	exc := Try(func() {
		if r.Method != http.MethodGet {
			ThrowHTTP(http.StatusMethodNotAllowed, "method not allowed")
		}

		path := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		parts := strings.SplitN(path, "/", 2)
		guid := parts[0]

		if guid == "" {
			ThrowHTTP(http.StatusBadRequest, "guid required")
		}

		if len(parts) == 2 && parts[1] == "output" {
			s.getOutput(w, r, guid)

			return
		}

		if len(parts) == 2 && parts[1] == "queued" {
			s.getQueued(w, r, guid)

			return
		}

		if len(parts) == 1 {
			s.getState(w, r, guid)

			return
		}

		ThrowHTTP(http.StatusNotFound, "not found")
	})

	exc.Catch(func(e *Exception) {
		sendHTTPException(w, e)
	})
}

// requireRoot reads ?root= from the URL. The S3 prefix is part of the
// protocol — without it the server would have to read the task body
// from etcd just to learn where result.json lives, which on ignite
// --wait polls meant pulling the full Script+Env on every tick.
// Callers must supply root.
func requireRoot(r *http.Request) string {
	root := r.URL.Query().Get("root")

	if root == "" {
		ThrowHTTP(http.StatusBadRequest, "root query param required")
	}

	return root
}

// getQueued is the cheapest possible "is this task still in the queue"
// probe: one etcd existence check (WithCountOnly so the body never
// crosses the wire) and a boolean answer. No root, no S3, no fallback.
// dedup-style callers use this instead of getState because they only
// need to know whether to fire a new task or not.
func (s *controlServer) getQueued(w http.ResponseWriter, r *http.Request, guid string) {
	resp := Throw2(s.etcd.Get(r.Context(), queueKey(guid), clientv3.WithCountOnly()))

	httpJSON(w, http.StatusOK, QueuedResp{GUID: guid, Queued: resp.Count > 0})
}

func (s *controlServer) getState(w http.ResponseWriter, r *http.Request, guid string) {
	root := requireRoot(r)
	state := "not_found"

	// WithCountOnly: server returns only resp.Count, skips Kvs entirely.
	// Without it, etcd ships the full Task body (Script + Env, up to
	// several MB for big scripts) on every ignite --wait poll — and
	// those polls are the bulk of control traffic. Existence is all we
	// need here.
	resp := Throw2(s.etcd.Get(r.Context(), queueKey(guid), clientv3.WithCountOnly()))

	if resp.Count > 0 {
		state = "queued"
	} else if s3ObjectExists(r.Context(), s.s3, s.bucket, resultKey(root, guid)) {
		state = "done"
	}

	httpJSON(w, http.StatusOK, StateResp{GUID: guid, State: state})
}

func (s *controlServer) getOutput(w http.ResponseWriter, r *http.Request, guid string) {
	root := requireRoot(r)
	result := s3GetBytes(r.Context(), s.s3, s.bucket, resultKey(root, guid))

	if result == nil {
		ThrowHTTP(http.StatusNotFound, "result.json not found for %s", guid)
	}

	stdout := s3GetBytes(r.Context(), s.s3, s.bucket, streamKey(root, guid, "stdout"))
	stderr := s3GetBytes(r.Context(), s.s3, s.bucket, streamKey(root, guid, "stderr"))

	httpJSON(w, http.StatusOK, OutputResp{
		Result:    result,
		StdoutB64: base64.StdEncoding.EncodeToString(stdout),
		StderrB64: base64.StdEncoding.EncodeToString(stderr),
	})
}

func s3ObjectExists(ctx context.Context, cli *s3.Client, bucket, key string) bool {
	_, err := cli.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	if err == nil {
		return true
	}

	if isS3NotFound(err) {
		return false
	}

	Throw(err)

	return false
}

func s3GetBytes(ctx context.Context, cli *s3.Client, bucket, key string) []byte {
	resp, err := cli.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		if isS3NotFound(err) {
			return nil
		}

		Throw(err)
	}

	defer resp.Body.Close()

	return Throw2(io.ReadAll(resp.Body))
}

func httpJSON(w http.ResponseWriter, status int, body any) {
	data := Throw2(json.Marshal(body))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	data, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(data)
}

// sendHTTPException is the boundary between our exception-style flow
// and HTTP status codes. ThrowHTTP raises a typed HTTPError; anything
// else (etcd timeout, S3 error, JSON unmarshal) is an unexpected
// failure and maps to 500.
func sendHTTPException(w http.ResponseWriter, e *Exception) {
	var he *HTTPError

	if errors.As(e.AsError(), &he) {
		httpError(w, he.Status, he.Msg)

		return
	}

	httpError(w, http.StatusInternalServerError, e.Error())
}
