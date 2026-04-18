package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	GUID string            `json:"guid,omitempty"`
	Cmd  []string          `json:"cmd"`
	Env  map[string]string `json:"env,omitempty"`
}

type EnqueueResp struct {
	GUID string `json:"guid"`
}

type StateResp struct {
	GUID  string `json:"guid"`
	State string `json:"state"`
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

	srv := &controlServer{etcd: cli, s3: s3cli, bucket: cfg.S3.Bucket}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tasks", srv.handleTasks)
	mux.HandleFunc("/v1/tasks/", srv.handleTaskByID)

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
	etcd   *clientv3.Client
	s3     *s3.Client
	bucket string
}

func (s *controlServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	exc := Try(func() {
		s.enqueue(w, r)
	})

	exc.Catch(func(e *Exception) {
		httpError(w, http.StatusInternalServerError, e.Error())
	})
}

func (s *controlServer) enqueue(w http.ResponseWriter, r *http.Request) {
	body := Throw2(io.ReadAll(r.Body))

	var req EnqueueReq
	Throw(json.Unmarshal(body, &req))

	if len(req.Cmd) == 0 {
		httpError(w, http.StatusBadRequest, "cmd is required")

		return
	}

	guid := req.GUID

	if guid == "" {
		guid = newGUID()
	}

	task := Task{GUID: guid, Cmd: req.Cmd, Env: req.Env}
	payload := Throw2(json.Marshal(task))
	key := queueKey(guid)

	resp := Throw2(s.etcd.Txn(r.Context()).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(payload))).
		Commit())

	if !resp.Succeeded {
		httpError(w, http.StatusConflict, fmt.Sprintf("task with guid %q already exists", guid))

		return
	}

	httpJSON(w, http.StatusOK, EnqueueResp{GUID: guid})
}

func (s *controlServer) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
	parts := strings.SplitN(path, "/", 2)
	guid := parts[0]

	if guid == "" {
		httpError(w, http.StatusBadRequest, "guid required")

		return
	}

	exc := Try(func() {
		if len(parts) == 2 && parts[1] == "output" {
			s.getOutput(w, r, guid)

			return
		}

		if len(parts) == 1 {
			s.getState(w, r, guid)

			return
		}

		httpError(w, http.StatusNotFound, "not found")
	})

	exc.Catch(func(e *Exception) {
		httpError(w, http.StatusInternalServerError, e.Error())
	})
}

func (s *controlServer) getState(w http.ResponseWriter, r *http.Request, guid string) {
	state := "not_found"

	resp := Throw2(s.etcd.Get(r.Context(), queueKey(guid)))

	if resp.Count > 0 {
		state = "queued"
	} else if s3ObjectExists(r.Context(), s.s3, s.bucket, resultKey(guid)) {
		state = "done"
	}

	httpJSON(w, http.StatusOK, StateResp{GUID: guid, State: state})
}

func (s *controlServer) getOutput(w http.ResponseWriter, r *http.Request, guid string) {
	result := s3GetBytes(r.Context(), s.s3, s.bucket, resultKey(guid))

	if result == nil {
		httpError(w, http.StatusNotFound, "result.json not found for "+guid)

		return
	}

	stdout := s3GetBytes(r.Context(), s.s3, s.bucket, streamKey(guid, "stdout"))
	stderr := s3GetBytes(r.Context(), s.s3, s.bucket, streamKey(guid, "stderr"))

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
