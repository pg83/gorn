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
	"sort"
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
	mux.HandleFunc("/endpoints", srv.handleEndpoints)
	mux.HandleFunc("/api/tasks", srv.handleAPITasks)

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
	Host       string
	Slots      int
	EnqueuedAt string
	Age        string
}

type pageData struct {
	Page      string
	Endpoints []EndpointInfo
	Tasks     []taskRow
	Running   int
	Waiting   int
	Oldest    string
	API       string
	Error     string
	Now       string
}

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>gorn {{.Page}}</title>
<style>
:root {
  color-scheme: light;
  --plane: #f9f9f7; --surface: #fcfcfb; --ink: #0b0b0b; --ink-2: #52514e;
  --muted: #898781; --hair: #e1e0d9; --border: rgba(11,11,11,0.10);
  --good: #0ca30c; --serious: #ec835a; --critical: #d03b3b;
  --accent: #2a78d6; --wash: rgba(42,120,214,0.08); --runrow: rgba(42,120,214,0.045);
  --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  --sans: system-ui, -apple-system, "Segoe UI", sans-serif;
}
@media (prefers-color-scheme: dark) {
  :root {
    color-scheme: dark;
    --plane: #0d0d0d; --surface: #1a1a19; --ink: #fff; --ink-2: #c3c2b7;
    --muted: #898781; --hair: #2c2c2a; --border: rgba(255,255,255,0.10);
    --accent: #3987e5; --wash: rgba(57,135,229,0.14); --runrow: rgba(57,135,229,0.09);
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--plane); color: var(--ink); font: 15px/1.55 var(--sans); }
.page { max-width: 1280px; margin: 0 auto; padding: 22px 20px 60px; }
.head { display: flex; align-items: baseline; gap: 16px; flex-wrap: wrap; margin-bottom: 18px; }
.brand { font-size: 20px; font-weight: 700; letter-spacing: -0.02em; }
.nav { display: flex; gap: 4px; }
.nav a { font-size: 13.5px; text-decoration: none; color: var(--ink-2); padding: 3px 11px; border-radius: 999px; }
.nav a:hover { background: var(--wash); }
.nav a.on { background: var(--accent); color: #fff; font-weight: 550; }
.meta { margin-left: auto; font: 11.5px var(--mono); color: var(--muted); display: inline-flex; align-items: center; gap: 6px; }
.pulse { width: 6px; height: 6px; border-radius: 50%; background: var(--good); }
.pulse.stale { background: var(--critical); }
.stats { display: flex; gap: 26px; flex-wrap: wrap; margin-bottom: 18px; }
.stat .k { font: 11px var(--sans); letter-spacing: .07em; text-transform: uppercase; color: var(--muted); }
.stat .v { font-size: 26px; font-weight: 650; letter-spacing: -0.02em; line-height: 1.2; font-variant-numeric: tabular-nums; }
.wrap { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-size: 13.5px; }
th { text-align: left; font: 600 11px var(--sans); letter-spacing: .07em; text-transform: uppercase; color: var(--muted);
     padding: 10px 12px; border-bottom: 1px solid var(--hair); white-space: nowrap; }
td { padding: 9px 12px; border-bottom: 1px solid var(--hair); vertical-align: baseline; }
tr:last-child td { border-bottom: 0; }
tr.running td { background: var(--runrow); }
tr.running td:first-child { box-shadow: inset 2px 0 0 var(--accent); }
.guid { font: 11.5px var(--mono); color: var(--muted); word-break: break-all; }
.descr { font: 12.5px var(--mono); color: var(--ink); white-space: pre-wrap; word-break: break-word; }
.ts { font: 11.5px var(--mono); color: var(--muted); white-space: nowrap; }
.num { font-variant-numeric: tabular-nums; white-space: nowrap; text-align: right; }
.host { font: 12.5px var(--mono); display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; color: var(--accent); }
.host .rd { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); flex: none; }
.dash { color: var(--muted); }
.old { color: var(--serious); font-weight: 600; }
.empty { color: var(--muted); padding: 14px 12px; }
.err { background: var(--surface); border: 1px solid color-mix(in srgb, var(--critical) 45%, transparent);
       border-left: 3px solid var(--critical); border-radius: 8px; padding: 11px 14px; margin-bottom: 16px; }
.err code { font: 12px var(--mono); color: var(--critical); word-break: break-all; }
</style>
</head>
<body>
<div class="page">
  <div class="head">
    <span class="brand">gorn</span>
    <nav class="nav">
      <a href="/" class="{{if eq .Page "queue"}}on{{end}}">Queue</a>
      <a href="/endpoints" class="{{if eq .Page "endpoints"}}on{{end}}">Endpoints</a>
    </nav>
    <span class="meta"><span class="pulse" id="pulse"></span><span id="stamp">{{.Now}}</span> · api {{.API}}</span>
  </div>

  {{if .Error}}<div class="err"><code>{{.Error}}</code></div>{{end}}

  {{if eq .Page "queue"}}
  <div class="stats">
    <div class="stat"><div class="k">running</div><div class="v" id="n-run">{{.Running}}</div></div>
    <div class="stat"><div class="k">waiting</div><div class="v" id="n-wait">{{.Waiting}}</div></div>
    <div class="stat"><div class="k">oldest</div><div class="v" id="n-old">{{.Oldest}}</div></div>
  </div>
  <div class="wrap">
  <table>
    <thead><tr>
      <th style="width:46%">task</th><th>host</th><th class="num">slots</th><th>enqueued</th><th class="num">age</th>
    </tr></thead>
    <tbody id="rows">
    {{range .Tasks}}
      <tr{{if .Host}} class="running"{{end}}>
        <td><div class="descr">{{.Descr}}</div><div class="guid">{{.GUID}}</div></td>
        <td>{{if .Host}}<span class="host"><span class="rd"></span>{{.Host}}</span>{{else}}<span class="dash">&mdash;</span>{{end}}</td>
        <td class="num">{{.Slots}}</td>
        <td class="ts">{{.EnqueuedAt}}</td>
        <td class="num age" data-at="{{.EnqueuedAt}}">{{.Age}}</td>
      </tr>
    {{else}}
      <tr><td colspan="5" class="empty">queue is empty</td></tr>
    {{end}}
    </tbody>
  </table>
  </div>
  {{else}}
  <div class="wrap">
  <table>
    <thead><tr><th style="width:28%">host</th><th class="num">port</th><th>user</th><th>path</th></tr></thead>
    <tbody>
    {{range .Endpoints}}
      <tr>
        <td class="descr">{{.Host}}</td>
        <td class="num">{{if .Port}}{{.Port}}{{else}}22{{end}}</td>
        <td class="descr">{{.User}}</td>
        <td class="guid">{{.Path}}</td>
      </tr>
    {{else}}
      <tr><td colspan="4" class="empty">no endpoints</td></tr>
    {{end}}
    </tbody>
  </table>
  </div>
  {{end}}
</div>

{{if eq .Page "queue"}}
<script>
// Refresh by patching the table, not by reloading the document: a
// meta-refresh drops text selection and scroll position every 2s. Ages
// tick locally each second so the numbers stay honest between polls.
(function () {
  var rows = document.getElementById('rows');
  var pulse = document.getElementById('pulse');
  var stamp = document.getElementById('stamp');

  function fmtAge(ms) {
    if (ms < 0) ms = 0;
    var s = Math.floor(ms / 1000);
    if (s < 60) return s + 's';
    var m = Math.floor(s / 60);
    if (m < 60) return m + 'm ' + (s % 60) + 's';
    var h = Math.floor(m / 60);
    return h + 'h ' + (m % 60) + 'm';
  }

  function tick() {
    var now = Date.now();
    var oldest = -1;
    var cells = rows.querySelectorAll('.age');
    for (var i = 0; i < cells.length; i++) {
      var at = Date.parse(cells[i].getAttribute('data-at'));
      if (isNaN(at)) continue;
      var d = now - at;
      cells[i].textContent = fmtAge(d);
      var running = cells[i].parentNode.classList.contains('running');
      cells[i].classList.toggle('old', !running && d > 60000);
      if (!running && d > oldest) oldest = d;
    }
    var el = document.getElementById('n-old');
    if (el) el.textContent = oldest < 0 ? '-' : fmtAge(oldest);
  }

  function cell(cls, text) {
    var td = document.createElement('td');
    if (cls) td.className = cls;
    td.textContent = text;
    return td;
  }

  function render(tasks) {
    var frag = document.createDocumentFragment();
    if (!tasks.length) {
      var tr = document.createElement('tr');
      var td = cell('empty', 'queue is empty');
      td.colSpan = 5;
      tr.appendChild(td);
      frag.appendChild(tr);
    }
    var running = 0;
    tasks.forEach(function (t) {
      if (t.host) running++;
      var tr = document.createElement('tr');
      if (t.host) tr.className = 'running';

      var first = document.createElement('td');
      var d = document.createElement('div');
      d.className = 'descr';
      d.textContent = t.descr || '';
      var g = document.createElement('div');
      g.className = 'guid';
      g.textContent = t.guid;
      first.appendChild(d);
      first.appendChild(g);
      tr.appendChild(first);

      var h = document.createElement('td');
      if (t.host) {
        var span = document.createElement('span');
        span.className = 'host';
        var dot = document.createElement('span');
        dot.className = 'rd';
        span.appendChild(dot);
        span.appendChild(document.createTextNode(t.host));
        h.appendChild(span);
      } else {
        var dash = document.createElement('span');
        dash.className = 'dash';
        dash.textContent = '—';
        h.appendChild(dash);
      }
      tr.appendChild(h);

      tr.appendChild(cell('num', t.slots || 1));
      tr.appendChild(cell('ts', t.enqueued_at || ''));
      var age = cell('num age', '');
      age.setAttribute('data-at', t.enqueued_at || '');
      tr.appendChild(age);

      frag.appendChild(tr);
    });
    rows.replaceChildren(frag);
    document.getElementById('n-run').textContent = running;
    document.getElementById('n-wait').textContent = tasks.length - running;
    tick();
  }

  function poll() {
    fetch('/api/tasks', {cache: 'no-store'})
      .then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
      .then(function (data) {
        pulse.classList.remove('stale');
        stamp.textContent = new Date().toISOString().replace(/\.\d+Z$/, 'Z');
        render(data.tasks || []);
      })
      .catch(function () { pulse.classList.add('stale'); });
  }

  setInterval(tick, 1000);
  setInterval(poll, 2000);
  tick();
})();
</script>
{{end}}
</body>
</html>`))

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	data := pageData{Page: "queue", API: s.api, Now: time.Now().UTC().Format(time.RFC3339), Oldest: "-"}

	exc := Try(func() {
		tasks := s.tasks(r.Context())
		now := time.Now().UTC()
		data.Tasks = make([]taskRow, len(tasks))

		oldest := time.Duration(-1)

		for i, t := range tasks {
			slots := t.Slots

			if slots <= 0 {
				slots = 1
			}

			data.Tasks[i] = taskRow{
				GUID:       t.GUID,
				Descr:      t.Descr,
				Host:       t.Host,
				Slots:      slots,
				EnqueuedAt: t.EnqueuedAt,
				Age:        taskAge(now, t.EnqueuedAt),
			}

			if t.Host != "" {
				data.Running++

				continue
			}

			data.Waiting++

			if d := waitedFor(now, t.EnqueuedAt); d > oldest {
				oldest = d
			}
		}

		if oldest >= 0 {
			data.Oldest = oldest.Truncate(time.Second).String()
		}
	})

	exc.Catch(func(e *Exception) {
		data.Error = e.Error()
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTmpl.Execute(w, data)
}

// tasks lists the queue with running tasks first, then in queue order.
// A task keeps its queue entry while it runs, so the two live in one list
// and a non-empty Host is what tells them apart.
func (s *webServer) tasks(ctx context.Context) []TaskListItem {
	var resp TaskListResp
	s.getJSON(ctx, "/v1/tasks", &resp)

	sort.SliceStable(resp.Tasks, func(i, j int) bool {
		a, b := resp.Tasks[i], resp.Tasks[j]

		if (a.Host != "") != (b.Host != "") {
			return a.Host != ""
		}

		return a.CreateRevision < b.CreateRevision
	})

	return resp.Tasks
}

// handleAPITasks is the browser's view of the queue: control usually listens
// on loopback, so the page cannot reach it directly and polls through here.
func (s *webServer) handleAPITasks(w http.ResponseWriter, r *http.Request) {
	exc := Try(func() {
		if r.Method != http.MethodGet {
			ThrowFmt("method not allowed")
		}

		httpJSON(w, http.StatusOK, TaskListResp{Tasks: s.tasks(r.Context())})
	})

	exc.Catch(func(e *Exception) {
		httpError(w, http.StatusBadGateway, e.Error())
	})
}

func (s *webServer) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/endpoints" {
		http.NotFound(w, r)

		return
	}

	data := pageData{Page: "endpoints", API: s.api, Now: time.Now().UTC().Format(time.RFC3339)}

	exc := Try(func() {
		var eps EndpointsResp
		s.getJSON(r.Context(), "/v1/endpoints", &eps)
		data.Endpoints = eps.Endpoints
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

// waitedFor is how long the task has been in the queue, or -1 when the
// timestamp is missing or unparsable.
func waitedFor(now time.Time, enqueuedAt string) time.Duration {
	if enqueuedAt == "" {
		return -1
	}

	ts, err := time.Parse(time.RFC3339Nano, enqueuedAt)

	if err != nil {
		return -1
	}

	return now.Sub(ts)
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
