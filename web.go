package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func webMain(args []string) {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	configPath := fs.String("config", "", "path to JSON config (required)")
	Throw(fs.Parse(args))

	if *configPath == "" {
		ThrowFmt("web: --config is required")
	}

	cfg := LoadConfig(*configPath)

	if cfg.Web.Listen == "" {
		ThrowFmt("web: web.listen is required in config")
	}

	if cfg.Web.API == "" {
		ThrowFmt("web: web.api is required in config")
	}

	srv := &webServer{
		api:  strings.TrimRight(cfg.Web.API, "/"),
		http: &http.Client{Timeout: 10 * time.Second},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)

	server := &http.Server{Addr: cfg.Web.Listen, Handler: mux}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

		sig := <-sigs
		fmt.Fprintln(os.Stderr, "web: received signal:", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = server.Shutdown(ctx)
	}()

	fmt.Fprintln(os.Stderr, "web: listening on", cfg.Web.Listen, "api=", srv.api)

	err := server.ListenAndServe()

	if err != nil && err != http.ErrServerClosed {
		Throw(err)
	}
}

type webServer struct {
	api  string
	http *http.Client
}

type taskRow struct {
	GUID       string
	Descr      string
	EnqueuedAt string
	Age        string
}

type indexData struct {
	Endpoints []EndpointInfo
	Tasks     []taskRow
	API       string
	Error     string
	Now       string
}

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="2">
<title>gorn cluster</title>
<link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
</head>
<body class="bg-light">
<div class="container py-4">
  <div class="d-flex justify-content-between align-items-baseline mb-3">
    <h1 class="mb-0">gorn cluster</h1>
    <small class="text-muted">refresh 2s · {{.Now}} · api {{.API}}</small>
  </div>

  {{if .Error}}
  <div class="alert alert-danger"><code>{{.Error}}</code></div>
  {{end}}

  <h3>Endpoints <span class="badge bg-secondary">{{len .Endpoints}}</span></h3>
  <table class="table table-sm table-striped table-bordered bg-white">
    <thead class="table-dark">
      <tr><th>host</th><th>port</th><th>user</th><th>path</th></tr>
    </thead>
    <tbody>
    {{range .Endpoints}}
      <tr><td><code>{{.Host}}</code></td><td>{{if .Port}}{{.Port}}{{else}}22{{end}}</td><td><code>{{.User}}</code></td><td><code>{{.Path}}</code></td></tr>
    {{else}}
      <tr><td colspan="4" class="text-muted">no endpoints</td></tr>
    {{end}}
    </tbody>
  </table>

  <h3 class="mt-4">Queue <span class="badge bg-secondary">{{len .Tasks}}</span></h3>
  <table class="table table-sm table-striped table-bordered bg-white">
    <thead class="table-dark">
      <tr><th>guid</th><th>descr</th><th>enqueued</th><th>age</th></tr>
    </thead>
    <tbody>
    {{range .Tasks}}
      <tr><td><code>{{.GUID}}</code></td><td><code>{{.Descr}}</code></td><td><small>{{.EnqueuedAt}}</small></td><td>{{.Age}}</td></tr>
    {{else}}
      <tr><td colspan="4" class="text-muted">queue is empty</td></tr>
    {{end}}
    </tbody>
  </table>
</div>
</body>
</html>`))

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	data := indexData{API: s.api, Now: time.Now().UTC().Format(time.RFC3339)}

	exc := Try(func() {
		var eps EndpointsResp
		s.getJSON(r.Context(), "/v1/endpoints", &eps)
		data.Endpoints = eps.Endpoints

		var tasks TaskListResp
		s.getJSON(r.Context(), "/v1/tasks", &tasks)

		now := time.Now().UTC()
		data.Tasks = make([]taskRow, len(tasks.Tasks))

		for i, t := range tasks.Tasks {
			descr := t.Descr

			if descr == "" {
				descr = strings.Join(t.Cmd, " ")
			}

			data.Tasks[i] = taskRow{
				GUID:       t.GUID,
				Descr:      descr,
				EnqueuedAt: t.EnqueuedAt,
				Age:        taskAge(now, t.EnqueuedAt),
			}
		}
	})

	exc.Catch(func(e *Exception) {
		data.Error = e.Error()
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTmpl.Execute(w, data)
}

func (s *webServer) getJSON(ctx context.Context, path string, out any) {
	// Short, bounded retry for transient backend errors (etcd timeout
	// surfaces as HTTP 500 from control). The browser refreshes every
	// 2s, so don't stall long; a handful of attempts is enough.
	const attempts = 4
	delay := 100 * time.Millisecond

	var lastErr error

	for i := 0; i < attempts; i++ {
		done, err := s.tryGetJSON(ctx, path, out)

		if done {
			return
		}

		lastErr = err

		if ctx.Err() != nil {
			break
		}

		time.Sleep(delay)
		delay *= 2
	}

	Throw(lastErr)
}

func (s *webServer) tryGetJSON(ctx context.Context, path string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+path, nil)

	if err != nil {
		return true, err
	}

	resp, err := s.http.Do(req)

	if err != nil {
		return false, err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)

	if err != nil {
		return false, err
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return false, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if resp.StatusCode != http.StatusOK {
		return true, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.Unmarshal(body, out); err != nil {
		return true, err
	}

	return true, nil
}

func taskAge(now time.Time, enqueuedAt string) string {
	if enqueuedAt == "" {
		return ""
	}

	ts, err := time.Parse(time.RFC3339Nano, enqueuedAt)

	if err != nil {
		return ""
	}

	return now.Sub(ts).Truncate(time.Second).String()
}
