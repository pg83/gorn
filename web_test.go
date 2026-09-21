package main

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebQueuePageOnlyFetchesAndRendersQueue(t *testing.T) {
	var taskCalls atomic.Int32
	var endpointCalls atomic.Int32

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tasks":
			taskCalls.Add(1)
			Throw(json.NewEncoder(w).Encode(TaskListResp{Tasks: []TaskListItem{{
				GUID:       "task-1",
				Descr:      "test task & its inputs",
				EnqueuedAt: "2026-09-19T19:43:52.123456789+03:00",
			}}}))
		case "/v1/endpoints":
			endpointCalls.Add(1)
			Throw(json.NewEncoder(w).Encode(EndpointsResp{Endpoints: []EndpointInfo{{
				Host: "worker-1",
			}}}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleIndex(res, httptest.NewRequest(http.MethodGet, "/", nil))
	body := res.Body.String()

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}

	if taskCalls.Load() != 1 || endpointCalls.Load() != 0 {
		t.Fatalf("API calls: tasks=%d endpoints=%d, want 1/0", taskCalls.Load(), endpointCalls.Load())
	}

	for _, want := range []string{"gorn queue", "task-1", "test task &amp; its inputs", `href="/endpoints"`} {
		if !strings.Contains(body, want) {
			t.Errorf("queue page does not contain %q", want)
		}
	}

	for _, want := range []string{`datetime="2026-09-19T19:43:52.123456789+03:00"`, `title="2026-09-19T19:43:52.123456789+03:00">16:43:52</time>`} {
		if !strings.Contains(html.UnescapeString(body), want) {
			t.Errorf("queue page lost the original timestamp or its UTC time: %q", want)
		}
	}

	if strings.Contains(body, "worker-1") || strings.Contains(body, "no endpoints") {
		t.Errorf("queue page contains endpoint data: %s", body)
	}
}

func TestWebEndpointsPageOnlyFetchesAndRendersEndpoints(t *testing.T) {
	var taskCalls atomic.Int32
	var endpointCalls atomic.Int32

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tasks":
			taskCalls.Add(1)
			Throw(json.NewEncoder(w).Encode(TaskListResp{Tasks: []TaskListItem{{
				GUID: "task-1",
			}}}))
		case "/v1/endpoints":
			endpointCalls.Add(1)
			Throw(json.NewEncoder(w).Encode(EndpointsResp{Endpoints: []EndpointInfo{{
				Host: "worker-1",
				Port: 2222,
				User: "gorn_1",
				Path: "/run/gorn_1",
			}}}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleEndpoints(res, httptest.NewRequest(http.MethodGet, "/endpoints", nil))
	body := res.Body.String()

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}

	if taskCalls.Load() != 0 || endpointCalls.Load() != 1 {
		t.Fatalf("API calls: tasks=%d endpoints=%d, want 0/1", taskCalls.Load(), endpointCalls.Load())
	}

	for _, want := range []string{"gorn endpoints", "worker-1", "2222", "gorn_1", "/run/gorn_1", `href="/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("endpoints page does not contain %q", want)
		}
	}

	if strings.Contains(body, "task-1") || strings.Contains(body, "queue is empty") {
		t.Errorf("endpoints page contains queue data: %s", body)
	}
}

func TestWebQueueListsRunningFirstAndMarksHost(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tasks" {
			http.NotFound(w, r)

			return
		}

		Throw(json.NewEncoder(w).Encode(TaskListResp{Tasks: []TaskListItem{
			{GUID: "waiting-first", Descr: "queued earlier", CreateRevision: 1},
			{GUID: "running-later", Descr: "already dispatched", Host: "worker-7", CreateRevision: 9},
		}}))
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleIndex(res, httptest.NewRequest(http.MethodGet, "/", nil))
	body := res.Body.String()

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}

	run := strings.Index(body, "running-later")
	wait := strings.Index(body, "waiting-first")

	if run < 0 || wait < 0 {
		t.Fatalf("both tasks should render, got run=%d wait=%d", run, wait)
	}

	if run > wait {
		t.Errorf("running task must sort above the waiting one")
	}

	if !strings.Contains(body, "worker-7") {
		t.Errorf("running task does not show its host")
	}

	// One running, one waiting — the counters say so.
	for _, want := range []string{`id="n-run">1<`, `id="n-wait">1<`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
}

func TestWebAPITasksProxiesQueue(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Throw(json.NewEncoder(w).Encode(TaskListResp{Tasks: []TaskListItem{
			{GUID: "t-1", Host: "worker-1", CreateRevision: 1},
		}}))
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleAPITasks(res, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}

	var got TaskListResp
	Throw(json.Unmarshal(res.Body.Bytes(), &got))

	if len(got.Tasks) != 1 || got.Tasks[0].Host != "worker-1" {
		t.Fatalf("proxy did not pass the queue through: %s", res.Body.String())
	}
}

func TestWebAPITasksFailsLoudWhenControlIsDown(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleAPITasks(res, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	if res.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", res.Code)
	}
}

func TestWebQueueRowsLinkToTaskPage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Throw(json.NewEncoder(w).Encode(TaskListResp{Tasks: []TaskListItem{{GUID: "task-1", Root: "fixer", Descr: "x"}}}))
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleIndex(res, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(res.Body.String(), `href="/tasks/task-1?root=fixer"`) {
		t.Fatalf("queue row is not a link to the task page: %s", res.Body.String())
	}
}

func TestWebTaskPageRendersControlInfo(t *testing.T) {
	var path string

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.String()
		Throw(json.NewEncoder(w).Encode(TaskInfo{
			GUID: "task-1", State: "queued", Root: "fixer", Descr: "updater_fixer run & co", Slots: 2, EnqueuedAt: "2026-09-21T08:23:47Z",
			EnvKeys: []string{"GORN_API", "S3_ENDPOINT"}, Host: "192.168.103.16", User: "gorn_0", Port: 9000, Cpus: 10,
		}))
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleTask(res, httptest.NewRequest(http.MethodGet, "/tasks/task-1?root=fixer", nil))
	body := res.Body.String()

	if res.Code != http.StatusOK || path != "/v1/tasks/task-1/info?root=fixer" {
		t.Fatalf("status=%d control path=%q", res.Code, path)
	}

	for _, want := range []string{"updater_fixer run &amp; co", `data-guid="task-1"`, `data-root="fixer"`, "gorn_0", "192.168.103.16:9000", `id="slots">2<`, `id="cpus">10<`, "2 keys", `title="GORN_API, S3_ENDPOINT"`, "08:23:47", `id="log"`} {
		if !strings.Contains(body, want) {
			t.Errorf("task page does not contain %q", want)
		}
	}
}

func TestWebTaskPageSurvivesControlOutage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "etcd down", http.StatusInternalServerError)
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}
	res := httptest.NewRecorder()
	srv.handleTask(res, httptest.NewRequest(http.MethodGet, "/tasks/task-1", nil))
	body := res.Body.String()

	if res.Code != http.StatusOK || !strings.Contains(body, "etcd down") || !strings.Contains(body, `data-guid="task-1"`) {
		t.Fatalf("outage page: status=%d body=%s", res.Code, body)
	}
}

func TestWebAPITaskProxiesInfoAndLogWithQuery(t *testing.T) {
	var paths []string

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())

		if strings.HasSuffix(r.URL.Path, "/log") {
			Throw(json.NewEncoder(w).Encode(LogResp{GUID: "task-1", Lines: []LogLine{{Cursor: "5", TS: "t", Stream: "stdout", Line: "hi"}}}))

			return
		}

		Throw(json.NewEncoder(w).Encode(TaskInfo{GUID: "task-1", State: "done"}))
	}))
	defer api.Close()

	srv := &webServer{api: api.URL, http: api.Client()}

	res := httptest.NewRecorder()
	srv.handleAPITask(res, httptest.NewRequest(http.MethodGet, "/api/tasks/task-1?root=fixer", nil))

	var info TaskInfo
	Throw(json.Unmarshal(res.Body.Bytes(), &info))

	res = httptest.NewRecorder()
	srv.handleAPITask(res, httptest.NewRequest(http.MethodGet, "/api/tasks/task-1/log?root=fixer&after=17&limit=500", nil))

	var lg LogResp
	Throw(json.Unmarshal(res.Body.Bytes(), &lg))

	if info.State != "done" || len(lg.Lines) != 1 || lg.Lines[0].Line != "hi" {
		t.Fatalf("proxy answers: info=%+v log=%+v", info, lg)
	}

	if len(paths) != 2 || paths[0] != "/v1/tasks/task-1/info?root=fixer" || paths[1] != "/v1/tasks/task-1/log?root=fixer&after=17&limit=500" {
		t.Fatalf("control paths: %v", paths)
	}

	res = httptest.NewRecorder()
	srv.handleAPITask(res, httptest.NewRequest(http.MethodGet, "/api/tasks/task-1/output", nil))

	if res.Code != http.StatusBadGateway {
		t.Fatalf("unknown resource status = %d, want 502", res.Code)
	}
}
