package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseTaskLogLineKeepsOnlyTheTask(t *testing.T) {
	line, ok := parseTaskLogLine("t-1", "170", `{"ts":"2026-09-21T08:23:48.4Z","guid":"t-1","root":"fixer","stream":"stderr","line":"+ clone","partial":true}`)

	if !ok || line.Stream != "stderr" || line.Line != "+ clone" || !line.Partial || line.Root != "fixer" || line.TS != "2026-09-21T08:23:48.4Z" || line.Cursor != "170" {
		t.Fatalf("json record: %+v ok=%v", line, ok)
	}

	line, ok = parseTaskLogLine("t-1", "171", "[2026-09-21T08:23:48.1Z] guid=t-1 wrap start: host=lab1 user=gorn_0")

	if !ok || line.Stream != "wrap" || line.Line != "wrap start: host=lab1 user=gorn_0" || line.TS != "2026-09-21T08:23:48.1Z" {
		t.Fatalf("wrap line: %+v ok=%v", line, ok)
	}

	for _, raw := range []string{
		`{"ts":"x","guid":"t-10","stream":"stdout","line":"other task"}`,
		"[2026-09-21T08:23:48.1Z] guid=t-10 wrap start",
		"not a log line",
		"{broken json",
	} {
		if _, ok := parseTaskLogLine("t-1", "1", raw); ok {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestLokiTaskLogQueriesAndSortsAscending(t *testing.T) {
	var got map[string]string

	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = map[string]string{}

		for k := range r.URL.Query() {
			got[k] = r.URL.Query().Get(k)
		}

		Throw(json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": []any{
			map[string]any{"values": [][2]string{
				{"1000000000000000002", "[2026-09-21T08:23:49Z] guid=t-1 running command"},
				{"1000000000000000001", `{"ts":"2026-09-21T08:23:48Z","guid":"t-1","root":"r","stream":"stdout","line":"hello"}`},
				{"1000000000000000003", `{"ts":"2026-09-21T08:23:50Z","guid":"t-11","stream":"stdout","line":"not mine"}`},
			}},
		}}}))
	}))
	defer loki.Close()

	c := &lokiClient{base: loki.URL, selector: `{service="gorn_task"}`, http: loki.Client()}
	start := time.Unix(0, 5)
	end := time.Unix(0, 9)
	lines := c.taskLog(context.Background(), "t-1", start, end, "backward", 7)

	if got["query"] != `{service="gorn_task"} |= "t-1"` || got["start"] != "5" || got["end"] != "9" || got["limit"] != "7" || got["direction"] != "backward" {
		t.Fatalf("query params: %v", got)
	}

	if len(lines) != 2 || lines[0].Line != "hello" || lines[1].Stream != "wrap" || lines[0].Root != "r" {
		t.Fatalf("lines: %+v", lines)
	}
}

func TestCursorLessComparesNumerically(t *testing.T) {
	if !cursorLess("999", "1000") || cursorLess("1000", "999") || cursorLess("5", "5") {
		t.Fatal("cursor order is not numeric")
	}
}

func TestInflightEntryAcceptsBareHost(t *testing.T) {
	var old InflightResp
	Throw(json.Unmarshal([]byte(`{"inflight":{"t-1":"worker-1"}}`), &old))

	var current InflightResp
	Throw(json.Unmarshal([]byte(`{"inflight":{"t-1":{"host":"worker-2","user":"gorn_3","port":9003,"cpus":5}}}`), &current))

	if old.Inflight["t-1"].Host != "worker-1" || current.Inflight["t-1"] != (InflightEntry{Host: "worker-2", User: "gorn_3", Port: 9003, Cpus: 5}) {
		t.Fatalf("old=%+v current=%+v", old.Inflight, current.Inflight)
	}
}
