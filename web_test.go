package main

import (
	"encoding/json"
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
				GUID:  "task-1",
				Descr: "test task",
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

	for _, want := range []string{"gorn queue", "task-1", "test task", `href="/endpoints"`} {
		if !strings.Contains(body, want) {
			t.Errorf("queue page does not contain %q", want)
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
