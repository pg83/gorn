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
	Error     string
	Now       string
}

var dashboardTmpl = template.Must(template.New("dashboard").Funcs(template.FuncMap{"clock": taskClock}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>gorn {{.Page}}</title>
<style>
:root{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;--bg:#f9f9f7;--rail:#f0f0ed;--text:#0b0b0b;--muted:#898781;--dim:#a4a29b;--line:#e1e0d9;--accent:#2a78d6;--rule:#d9d8d1;--runrow:rgba(42,120,214,.035);--hover:rgba(11,11,11,.025);--host:#52514e;--good:#0ca30c;--mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;color:var(--text);background:var(--bg);font-synthesis:none;color-scheme:light}
@media(prefers-color-scheme:dark){:root{--bg:#0d0d0d;--rail:#161615;--text:#f0f0ed;--muted:#898781;--dim:#5e5d57;--line:#242423;--accent:#3987e5;--rule:#2c2c2a;--runrow:rgba(57,135,229,.045);--hover:rgba(255,255,255,.025);--host:#c3c2b7;color-scheme:dark}}
*{box-sizing:border-box}
body{margin:0;display:flex;height:100dvh;overflow:hidden}
button,a{-webkit-tap-highlight-color:transparent}
button{font:inherit;cursor:pointer;color:inherit}
button:focus-visible,a:focus-visible{outline:1px solid var(--accent);outline-offset:4px}
button{border:0;background:none}
.sidebar{width:152px;flex:0 0 152px;display:flex;flex-direction:column;align-items:stretch;padding:24px 16px;background:var(--rail);gap:28px;overflow-y:auto}
.logo{display:flex;align-items:center;gap:11px;flex-shrink:0;font-size:24px;font-weight:650;letter-spacing:-1px;text-decoration:none;color:var(--text)}
.logo-icon{display:block;position:relative;width:35px;height:35px;background:var(--accent);color:#fff;line-height:32px;text-align:center;font-weight:500;font-size:25px;letter-spacing:-1px}
.logo-icon span{position:absolute;font-size:14px;right:3px;top:-4px}
nav{display:flex;flex-direction:column}
nav a{display:flex;align-items:center;gap:6px;min-height:44px;padding:12px 10px;text-align:left;text-decoration:none;border-left:2px solid transparent;font-size:12px;color:var(--muted)}
nav a:hover{color:var(--text)}
nav a.active{color:var(--accent);border-left-color:var(--accent)}
.nav-count{margin-left:auto;font:10px var(--mono);color:var(--dim)}
.active .nav-count{color:var(--accent);opacity:.65}
.queue-state{padding:0 0 0 12px;display:flex;flex-direction:column;gap:5px}
.filter{padding:7px 0;display:flex;align-items:center;gap:8px;font-size:11px;text-align:left;color:var(--muted)}
.filter:hover,.filter.selected{color:var(--text)}
.filter .count{margin-left:auto;font:11px var(--mono);font-variant-numeric:tabular-nums}
.filter.selected .count{color:var(--accent)}
.dot{display:inline-block;flex:none;width:5px;height:5px;border-radius:50%;background:var(--accent)}
.dot.waiting{background:transparent;border:1px solid var(--muted)}
.sidebar-foot{margin-top:auto;padding:0 0 0 12px;font-size:10px;line-height:1.9;color:var(--muted)}
.sidebar-foot .snapshot{display:flex;align-items:center;gap:7px}
.sidebar-foot .dot{background:var(--good)}
.sidebar-foot time{display:block;padding-left:12px;font:9px/1.9 var(--mono);color:var(--dim)}
main{flex:1;min-width:0;height:100dvh;overflow:auto;scrollbar-color:var(--rule) transparent;background:var(--bg)}
.page{padding:26px 36px 48px;min-height:100%;min-width:720px}
table{border-collapse:collapse;width:100%;table-layout:fixed;text-align:left}
col.task{width:53%}col.host{width:19%}col.slots{width:7%}col.time{width:12%}col.age{width:9%}
th{height:34px;padding:0 14px 13px;font:500 9px var(--mono);letter-spacing:1.25px;color:var(--muted);text-transform:uppercase;vertical-align:top;white-space:nowrap;border-bottom:1px solid var(--rule)}
th:first-child{padding-left:16px}th:last-child{padding-right:16px}
td{height:72px;padding:14px;vertical-align:middle;border-bottom:1px solid var(--line);font-size:12px}
td:first-child{padding-left:16px}td:last-child{padding-right:16px}
tr.running{background:var(--runrow)}tbody tr:hover{background:var(--hover)}
.task-line{display:flex;align-items:center;gap:11px;min-width:0}
.task-line .dot{width:5px;height:5px}
.task-body{min-width:0}
.task-name{white-space:pre-wrap;font:12px/1.55 var(--mono);color:var(--text);overflow-wrap:anywhere}
.task-guid{margin-top:4px;font:10px/1.4 var(--mono);color:var(--dim);overflow-wrap:anywhere}
.host-name{overflow-wrap:anywhere;font:12px/1.5 var(--mono);color:var(--accent)}
.number{text-align:right;font-variant-numeric:tabular-nums}
td.number{font:11px var(--mono);color:var(--muted)}
.clock{font:11px var(--mono);color:var(--muted);white-space:nowrap}
.dash{font:12px var(--mono);color:var(--dim)}
.running .age{color:var(--text)}
.host-col .host-name{color:var(--text)}
.endpoint-user,.endpoint-path{font:11px/1.5 var(--mono);color:var(--muted);overflow-wrap:anywhere}
.endpoints th:nth-child(1){width:28%}.endpoints th:nth-child(2){width:11%}.endpoints th:nth-child(3){width:19%}.endpoints th:nth-child(4){width:42%}
.endpoints td{height:64px}
.empty{height:130px;font:12px var(--mono);color:var(--muted);text-align:center}
[hidden]{display:none!important}
@media(min-width:1700px){col.task{width:57%}col.host{width:17%}col.slots{width:6%}col.time{width:11%}col.age{width:9%}.page{padding-left:44px;padding-right:44px}}
@media(max-width:1000px){.page{padding-left:22px;padding-right:22px}col.task{width:47%}col.host{width:20%}col.slots{width:8%}col.time{width:14%}col.age{width:11%}.task-name{font-size:11px}td{padding-left:10px;padding-right:10px}}
@media(max-width:680px){.sidebar{width:124px;flex-basis:124px;padding:20px 12px;gap:25px}.logo{font-size:21px;gap:8px}.logo-icon{width:30px;height:30px;line-height:28px;font-size:22px}.page{padding:22px 16px 40px;min-width:680px}.queue-state,.sidebar-foot{padding-left:12px}}

.sidebar-foot .dot.stale{background:#d03b3b}
.number.old{color:#ec835a}
.error{border-left:2px solid #d03b3b;padding:10px 14px;margin:0 0 18px;color:#d03b3b;font:12px/1.6 var(--mono);white-space:pre-wrap;overflow-wrap:anywhere}
</style>
</head>
<body>
<aside class="sidebar" aria-label="Navigation">
  <a class="logo" href="/" aria-label="Gorn queue"><span class="logo-icon" aria-hidden="true">g<span>↗</span></span>gorn</a>
  <nav aria-label="Views">
    <a href="/"{{if eq .Page "queue"}} class="active" aria-current="page"{{end}}>Queue{{if eq .Page "queue"}} <span class="nav-count" id="queue-count">{{len .Tasks}}</span>{{end}}</a>
    <a href="/endpoints"{{if eq .Page "endpoints"}} class="active" aria-current="page"{{end}}>Endpoints</a>
  </nav>
  {{if eq .Page "queue"}}
  <div class="queue-state" aria-label="Filter tasks">
    <button class="filter" data-filter="running" aria-pressed="false"><span class="dot" aria-hidden="true"></span>Running<span class="count" id="n-run">{{.Running}}</span></button>
    <button class="filter" data-filter="waiting" aria-pressed="false"><span class="dot waiting" aria-hidden="true"></span>Waiting<span class="count" id="n-wait">{{.Waiting}}</span></button>
  </div>
  {{end}}
  <div class="sidebar-foot">
    <span class="snapshot"><span class="dot{{if .Error}} stale{{end}}" id="pulse" aria-hidden="true"></span><span id="connection" role="status">{{if .Error}}Unavailable{{else if eq .Page "queue"}}Live{{else}}Snapshot{{end}}</span></span>
    <time id="stamp" datetime="{{.Now}}" title="{{.Now}}">{{clock .Now}} UTC</time>
  </div>
</aside>
<main>
<section class="page" aria-label="{{.Page}}">
  <div class="error" id="error" role="alert"{{if not .Error}} hidden{{end}}>{{.Error}}</div>
  {{if eq .Page "queue"}}
  <table aria-label="Task queue">
    <colgroup><col class="task"><col class="host"><col class="slots"><col class="time"><col class="age"></colgroup>
    <thead><tr><th scope="col">Task</th><th scope="col">Host</th><th scope="col" class="number">Slots</th><th scope="col" class="number">Enqueued</th><th scope="col" class="number">Age</th></tr></thead>
    <tbody id="rows">
    {{range .Tasks}}
      <tr class="{{if .Host}}running{{else}}waiting{{end}}" data-task>
        <td><div class="task-line"><span class="dot{{if not .Host}} waiting{{end}}" title="{{if .Host}}Running{{else}}Waiting{{end}}"></span><div class="task-body"><div class="task-name">{{.Descr}}</div><div class="task-guid">{{.GUID}}</div></div></div></td>
        <td>{{if .Host}}<span class="host-name">{{.Host}}</span>{{else}}<span class="dash">—</span>{{end}}</td>
        <td class="number">{{.Slots}}</td>
        <td class="number"><time class="clock" datetime="{{.EnqueuedAt}}" title="{{.EnqueuedAt}}">{{clock .EnqueuedAt}}</time></td>
        <td class="number age" data-at="{{.EnqueuedAt}}">{{.Age}}</td>
      </tr>
    {{end}}
      <tr id="empty-row"{{if .Tasks}} hidden{{end}}><td colspan="5" class="empty">queue is empty</td></tr>
    </tbody>
  </table>
  {{else}}
  <table class="endpoints" aria-label="Worker endpoints">
    <thead><tr><th scope="col">Host</th><th scope="col" class="number">Port</th><th scope="col">User</th><th scope="col">Path</th></tr></thead>
    <tbody>
    {{range .Endpoints}}
      <tr>
        <td class="host-col"><span class="host-name">{{.Host}}</span></td>
        <td class="number">{{if .Port}}{{.Port}}{{else}}22{{end}}</td>
        <td class="endpoint-user">{{.User}}</td>
        <td class="endpoint-path">{{.Path}}</td>
      </tr>
    {{else}}
      <tr><td colspan="4" class="empty">no endpoints</td></tr>
    {{end}}
    </tbody>
  </table>
  {{end}}
</section>
</main>
{{if eq .Page "queue"}}
<script>
(function () {
  var rows = document.getElementById('rows');
  var pulse = document.getElementById('pulse');
  var stamp = document.getElementById('stamp');
  var connection = document.getElementById('connection');
  var error = document.getElementById('error');
  var filters = document.querySelectorAll('[data-filter]');
  var filter = 'all';

  function clock(value) {
    var date = new Date(value);
    if (isNaN(date.getTime())) return value;
    return date.toLocaleTimeString('en-GB', {timeZone: 'UTC', hour12: false});
  }

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
    rows.querySelectorAll('.age').forEach(function (cell) {
      var at = Date.parse(cell.getAttribute('data-at'));
      if (isNaN(at)) return;
      var age = now - at;
      cell.textContent = fmtAge(age);
      cell.classList.toggle('old', !cell.parentNode.classList.contains('running') && age > 60000);
    });
  }

  function applyFilter() {
    var visible = 0;
    rows.querySelectorAll('[data-task]').forEach(function (row) {
      row.hidden = filter !== 'all' && !row.classList.contains(filter);
      if (!row.hidden) visible++;
    });
    var empty = document.getElementById('empty-row');
    empty.hidden = visible !== 0;
    empty.firstElementChild.textContent = filter === 'all' ? 'queue is empty' : 'no ' + filter + ' tasks';
    filters.forEach(function (button) {
      var active = button.dataset.filter === filter;
      button.classList.toggle('selected', active);
      button.setAttribute('aria-pressed', String(active));
    });
  }

  filters.forEach(function (button) {
    button.addEventListener('click', function () {
      filter = filter === button.dataset.filter ? 'all' : button.dataset.filter;
      applyFilter();
    });
  });

  function element(tag, cls, text) {
    var node = document.createElement(tag);
    node.className = cls;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function render(tasks) {
    var fragment = document.createDocumentFragment();
    var running = 0;
    tasks.forEach(function (task) {
      if (task.host) running++;
      var row = element('tr', task.host ? 'running' : 'waiting');
      row.setAttribute('data-task', '');
      var first = element('td', '');
      var line = element('div', 'task-line');
      var dot = element('span', task.host ? 'dot' : 'dot waiting');
      dot.title = task.host ? 'Running' : 'Waiting';
      var body = element('div', 'task-body');
      body.append(element('div', 'task-name', task.descr || ''), element('div', 'task-guid', task.guid));
      line.append(dot, body);
      first.appendChild(line);
      row.appendChild(first);

      var host = element('td', '');
      host.appendChild(element('span', task.host ? 'host-name' : 'dash', task.host || '—'));
      row.append(host, element('td', 'number', task.slots > 0 ? task.slots : 1));
      var enqueued = element('td', 'number');
      var time = element('time', 'clock', clock(task.enqueued_at || ''));
      time.dateTime = task.enqueued_at || '';
      time.title = time.dateTime;
      enqueued.appendChild(time);
      var age = element('td', 'number age', '');
      age.setAttribute('data-at', time.dateTime);
      row.append(enqueued, age);
      fragment.appendChild(row);
    });
    var empty = element('tr', '');
    empty.id = 'empty-row';
    var message = element('td', 'empty', 'queue is empty');
    message.colSpan = 5;
    empty.appendChild(message);
    fragment.appendChild(empty);
    rows.replaceChildren(fragment);
    document.getElementById('queue-count').textContent = tasks.length;
    document.getElementById('n-run').textContent = running;
    document.getElementById('n-wait').textContent = tasks.length - running;
    applyFilter();
    tick();
  }

  function poll() {
    fetch('/api/tasks', {cache: 'no-store'})
      .then(function (response) {
        if (response.ok) return response.json();
        return response.text().then(function (body) { throw new Error('HTTP ' + response.status + ': ' + body); });
      })
      .then(function (data) {
        render(data.tasks || []);
        pulse.classList.remove('stale');
        connection.textContent = 'Live';
        stamp.dateTime = new Date().toISOString();
        stamp.title = stamp.dateTime;
        stamp.textContent = clock(stamp.dateTime) + ' UTC';
        error.hidden = true;
        error.textContent = '';
      })
      .catch(function (failure) {
        pulse.classList.add('stale');
        connection.textContent = 'Unavailable';
        error.textContent = String(failure);
        error.hidden = false;
      });
  }

  setInterval(tick, 1000);
  setInterval(poll, 2000);
  tick();
})();
</script>
{{end}}
</body>
</html>
`))

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	data := pageData{Page: "queue", Now: time.Now().UTC().Format(time.RFC3339)}

	exc := Try(func() {
		tasks := s.tasks(r.Context())
		now := time.Now().UTC()
		data.Tasks = make([]taskRow, len(tasks))

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

	data := pageData{Page: "endpoints", Now: time.Now().UTC().Format(time.RFC3339)}

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

func taskClock(timestamp string) string {
	t, err := time.Parse(time.RFC3339Nano, timestamp)

	if err != nil {
		return timestamp
	}

	return t.UTC().Format("15:04:05")
}
