package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var taskTmpl = template.Must(template.New("task").Funcs(pageFuncs).Parse(pageHead + `
.page.task{display:flex;flex-direction:column;height:100%;min-height:0;padding-bottom:0}
.crumbs{display:flex;align-items:center;font:11px var(--mono);color:var(--muted);margin-bottom:12px}
.crumbs a{color:var(--muted);text-decoration:none}.crumbs a:hover{color:var(--text)}
.crumbs .sep{margin:0 8px;color:var(--dim)}
.crumbs .cur{color:var(--text);overflow-wrap:anywhere}
.crumbs .copy{margin-left:10px;font:9px var(--mono);letter-spacing:1px;text-transform:uppercase;color:var(--dim);padding:0}
.crumbs .copy:hover{color:var(--accent)}
.title{display:grid;grid-template-columns:120px 1fr;gap:0 18px;align-items:start;padding-bottom:12px}
.state{display:inline-flex;align-items:center;gap:8px;height:22px;padding:0 10px 0 9px;font:500 9px var(--mono);letter-spacing:1.25px;text-transform:uppercase;color:var(--accent);border:1px solid currentColor;border-radius:2px;white-space:nowrap}
.state .dot{background:currentColor}
.state.done{color:var(--good)}.state.failed{color:#d03b3b}.state.waiting,.state.not_found{color:var(--muted)}
.title .task-name{font:12px/22px var(--mono)}
.facts{display:grid;grid-template-columns:1fr 1fr 1.4fr;border-top:1px solid var(--rule);border-bottom:1px solid var(--rule)}
.facts section{padding:9px 16px 10px 0;margin-right:16px;border-right:1px solid var(--line);min-width:0}
.facts section:last-child{border-right:0;margin-right:0;padding-right:0}
.facts h2{margin:0 0 6px;font:500 9px var(--mono);letter-spacing:1.25px;text-transform:uppercase;color:var(--muted)}
.facts p{margin:0;font:11px/1.7 var(--mono);color:var(--text);overflow-wrap:anywhere;font-variant-numeric:tabular-nums;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.facts .muted{color:var(--muted)}
.facts .k{display:inline-block;width:72px;color:var(--muted)}
.facts .host-name{color:var(--accent)}
.facts .live{color:var(--accent)}
.facts .sep{color:var(--dim);margin:0 6px}
.logs{display:flex;flex-direction:column;flex:1;min-height:0}
.log{flex:1;min-height:0;overflow:auto;scrollbar-color:var(--rule) transparent;font:11px/1.6 var(--mono);padding:10px 0 0}
.ln{display:grid;grid-template-columns:max-content 46px 1fr;gap:0 14px;padding:1px 6px 1px 0;white-space:pre-wrap;overflow-wrap:anywhere}
.ln:hover{background:var(--hover)}
.ln .ts{color:var(--dim);font-variant-numeric:tabular-nums}
.ln .tag{color:var(--dim);font-size:9px;letter-spacing:1px;text-transform:uppercase;padding-top:2px}
.ln.wrap .tag{color:var(--accent)}.ln.stderr .tag{color:#ec835a}.ln.stdout .tag{color:var(--muted)}
.ln.wrap .tx{color:var(--muted)}
.ln .part{color:var(--dim);font-size:9px;letter-spacing:1px}
.ln.note .tx{color:var(--dim);font-style:italic;padding:6px 0}
.ln.note{background:var(--runrow)}
.ln.cursor .tx{color:var(--accent)}
.ln.cursor .tx::after{content:"▌";animation:pulse 1s steps(1) infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.25}}
.status{display:flex;justify-content:space-between;align-items:center;padding:6px 0 10px;font:10px var(--mono);color:var(--dim)}
.status b{font-weight:500;color:var(--muted)}
@media(max-width:1000px){.ln{gap:0 10px}}
</style>
</head>
<body>
<aside class="sidebar" aria-label="Navigation">
  <a class="logo" href="/" aria-label="Gorn queue"><span class="logo-icon" aria-hidden="true">g<span>↗</span></span>gorn</a>
  <nav aria-label="Views">
    <a href="/">Queue</a>
    <a href="/endpoints">Endpoints</a>
  </nav>
  <div class="sidebar-foot">
    <span class="snapshot"><span class="dot{{if .Error}} stale{{end}}" id="pulse" aria-hidden="true"></span><span id="connection" role="status">{{if .Error}}Unavailable{{else}}Live{{end}}</span></span>
    <time id="stamp" datetime="{{.Now}}" title="{{.Now}}">{{clock .Now}} UTC</time>
  </div>
</aside>
<main>
<section class="page task" aria-label="task" id="task" data-guid="{{.Task.GUID}}" data-root="{{.Task.Root}}" data-state="{{.Task.State}}" data-enqueued="{{.Task.EnqueuedAt}}">
  <div class="error" id="error" role="alert"{{if not .Error}} hidden{{end}}>{{.Error}}</div>
  <div class="crumbs"><a href="/">Queue</a><span class="sep">/</span><span id="crumb-root">{{.Task.Root}}</span><span class="sep">/</span><span class="cur">{{.Task.GUID}}</span><button class="copy" id="copy" title="Copy GUID">copy</button></div>
  <header class="title">
    <span class="state {{.Task.State}}" id="state"><span class="dot" aria-hidden="true"></span><span id="state-text">{{.Task.State}}</span></span>
    <div class="task-body"><div class="task-name" id="descr">{{.Task.Descr}}</div></div>
  </header>
  <div class="facts" aria-label="Task facts">
    <section>
      <h2>Worker</h2>
      <p id="worker">{{if .Task.User}}<span class="host-name">{{.Task.User}}</span> <span class="muted">@</span> {{.Task.Host}}{{else if .Task.Host}}{{.Task.Host}}{{else}}<span class="muted">not dispatched</span>{{end}}</p>
      <p id="address" class="muted">{{if .Task.Port}}{{.Task.Host}}:{{.Task.Port}}{{end}}</p>
      <p id="root"><span class="k">root</span>{{.Task.Root}}</p>
    </section>
    <section>
      <h2>Resources</h2>
      <p><span class="k">slots</span><span id="slots">{{if .Task.Slots}}{{.Task.Slots}}{{else}}1{{end}}</span></p>
      <p><span class="k">cpus</span><span id="cpus">{{if .Task.Cpus}}{{.Task.Cpus}}{{else}}—{{end}}</span></p>
      <p><span class="k">env</span><span id="env" title="{{range $i, $k := .Task.EnvKeys}}{{if $i}}, {{end}}{{$k}}{{end}}">{{len .Task.EnvKeys}} keys</span></p>
    </section>
    <section>
      <h2>Timeline</h2>
      <p><span class="k">enqueued</span><span id="enqueued">{{if .Task.EnqueuedAt}}{{clock .Task.EnqueuedAt}}{{else}}—{{end}}</span></p>
      <p><span class="k">started</span><span id="started">{{if .Task.Result}}{{clock .Task.Result.StartedAt}}{{else}}—{{end}}</span></p>
      <p id="outcome">{{if .Task.Result}}<span class="k">finished</span>{{clock .Task.Result.FinishedAt}} <span class="sep">·</span>exit {{.Task.Result.ExitCode}}{{else}}<span class="k">running</span><span class="muted">—</span>{{end}}</p>
    </section>
  </div>
  <section class="logs" aria-label="Task log">
    <div class="log" id="log" tabindex="0"></div>
    <div class="status"><span><b id="count">0</b> lines · <span id="log-state">loading</span></span><span>newest at bottom</span></div>
  </section>
</section>
</main>
<script>
(function () {
  var page = document.getElementById('task');
  var guid = page.dataset.guid;
  var root = page.dataset.root;
  var state = page.dataset.state;
  var enqueuedAt = page.dataset.enqueued;
  var log = document.getElementById('log');
  var count = document.getElementById('count');
  var logState = document.getElementById('log-state');
  var pulse = document.getElementById('pulse');
  var connection = document.getElementById('connection');
  var stamp = document.getElementById('stamp');
  var error = document.getElementById('error');
  var first = '';
  var last = '';
  var lines = 0;
  var reachedStart = false;
  var loading = false;
  var startedAt = '';
  var finishedAt = '';
  var updated = 0;
  var limit = 500;

  function clock(value) {
    var date = new Date(value);
    if (isNaN(date.getTime())) return value || '—';
    return date.toLocaleTimeString('en-GB', {timeZone: 'UTC', hour12: false});
  }
  function stampOf(value) {
    var date = new Date(value);
    if (isNaN(date.getTime())) return '';
    return date.toLocaleTimeString('en-GB', {timeZone: 'UTC', hour12: false}) + '.' + String(date.getMilliseconds()).padStart(3, '0');
  }
  function fmtDur(ms) {
    if (ms < 0) ms = 0;
    var s = Math.floor(ms / 1000);
    if (s < 60) return s + 's';
    var m = Math.floor(s / 60);
    if (m < 60) return m + 'm ' + (s % 60) + 's';
    var h = Math.floor(m / 60);
    return h + 'h ' + (m % 60) + 'm';
  }
  function text(id, value) { document.getElementById(id).textContent = value; }
  function query(params) {
    var q = new URLSearchParams();
    q.set('root', root);
    if (enqueuedAt) q.set('since', enqueuedAt);
    Object.keys(params).forEach(function (k) { q.set(k, params[k]); });
    return '/api/tasks/' + encodeURIComponent(guid) + '/log?' + q.toString();
  }
  function getJSON(url) {
    return fetch(url, {cache: 'no-store'}).then(function (response) {
      if (response.ok) return response.json();
      return response.text().then(function (body) { throw new Error('HTTP ' + response.status + ': ' + body); });
    });
  }
  function online(ok, failure) {
    pulse.classList.toggle('stale', !ok);
    connection.textContent = ok ? 'Live' : 'Unavailable';
    error.hidden = ok;
    error.textContent = ok ? '' : String(failure);
    if (ok) {
      stamp.dateTime = new Date().toISOString();
      stamp.title = stamp.dateTime;
      stamp.textContent = clock(stamp.dateTime) + ' UTC';
    }
  }
  function element(tag, cls, content) {
    var node = document.createElement(tag);
    node.className = cls;
    if (content !== undefined) node.textContent = content;
    return node;
  }
  function row(line) {
    var ln = element('div', 'ln ' + line.stream);
    ln.dataset.cursor = line.cursor;
    ln.append(element('span', 'ts', stampOf(line.ts)), element('span', 'tag', line.stream), element('span', 'tx', line.line));
    if (line.partial) ln.lastChild.appendChild(element('span', 'part', ' ⏎ chunk'));
    return ln;
  }
  function note(message) {
    var ln = element('div', 'ln note');
    ln.append(element('span', 'ts', ' '), element('span', 'tag', ''), element('span', 'tx', message));
    return ln;
  }
  function observe(line) {
    if (line.stream === 'wrap' && line.line.indexOf('wrap start:') === 0 && (!startedAt || line.ts < startedAt)) startedAt = line.ts;
    if (line.stream === 'wrap' && line.line.indexOf('command finished:') === 0) finishedAt = line.ts;
  }
  function add(list, prepend) {
    if (!list.length) return;
    var fragment = document.createDocumentFragment();
    list.forEach(function (line) { observe(line); fragment.appendChild(row(line)); });
    var atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 8;
    if (prepend) {
      var height = log.scrollHeight;
      log.prepend(fragment);
      log.scrollTop += log.scrollHeight - height;
      first = list[0].cursor;
    } else {
      var cursor = log.querySelector('.cursor');
      if (cursor) cursor.remove();
      log.appendChild(fragment);
      if (state === 'queued') log.appendChild(element('div', 'ln cursor'));
      if (atBottom) log.scrollTop = log.scrollHeight;
      last = list[list.length - 1].cursor;
      if (!first) first = list[0].cursor;
    }
    lines += list.length;
    count.textContent = lines;
    timeline();
  }
  function timeline() {
    text('started', startedAt ? clock(startedAt) : '—');
    if (state === 'queued') {
      var since = startedAt || enqueuedAt;
      var live = document.getElementById('outcome');
      if (!startedAt && !page.dataset.host) {
        live.innerHTML = '';
        live.append(element('span', 'k', 'waiting'), element('span', 'live', since ? fmtDur(Date.now() - Date.parse(since)) : '—'), element('span', 'muted', ' in queue'));
      } else {
        live.innerHTML = '';
        live.append(element('span', 'k', 'running'), element('span', 'live', since ? fmtDur(Date.now() - Date.parse(since)) : '—'), element('span', 'muted', ' so far'));
      }
    }
    var ago = updated ? Math.round((Date.now() - updated) / 1000) : 0;
    logState.textContent = state === 'queued' ? 'live, updated ' + ago + 's ago' : state === 'done' ? 'complete' : state === 'not_found' ? 'no record of this task' : 'loading';
  }
  function tail() {
    if (loading) return Promise.resolve();
    loading = true;
    return getJSON(query(last ? {after: last, limit: limit} : {limit: limit}))
      .then(function (data) {
        var fresh = data.lines || [];
        if (!last && fresh.length < limit) reachedStart = true;
        add(fresh, false);
        if (!root && data.root) { root = data.root; page.dataset.root = root; text('crumb-root', root); }
        updated = Date.now();
        online(true);
      })
      .catch(function (failure) { online(false, failure); })
      .then(function () { loading = false; timeline(); });
  }
  function older() {
    if (loading || reachedStart || !first) return;
    loading = true;
    getJSON(query({before: first, limit: limit}))
      .then(function (data) {
        var more = data.lines || [];
        if (more.length < limit) { reachedStart = true; log.prepend(note('beginning of log')); }
        add(more, true);
      })
      .catch(function (failure) { online(false, failure); })
      .then(function () { loading = false; });
  }
  function render(info) {
    state = info.state;
    page.dataset.state = state;
    page.dataset.host = info.host || '';
    var badge = document.getElementById('state');
    var label = state;
    if (state === 'queued') label = info.host ? 'running' : 'waiting';
    if (state === 'done' && info.result && info.result.exit_code !== 0) label = 'failed';
    badge.className = 'state ' + (state === 'queued' ? (info.host ? 'running' : 'waiting') : label);
    text('state-text', label + (state === 'done' && info.result ? ' · exit ' + info.result.exit_code : ''));
    if (info.descr) text('descr', info.descr);
    var worker = document.getElementById('worker');
    worker.innerHTML = '';
    if (info.user) {
      worker.append(element('span', 'host-name', info.user), element('span', 'muted', ' @ '), document.createTextNode(info.host || ''));
    } else if (info.host) {
      worker.textContent = info.host;
    } else {
      worker.appendChild(element('span', 'muted', 'not dispatched'));
    }
    text('address', info.port ? info.host + ':' + info.port : '');
    if (info.root) {
      root = info.root;
      var rootCell = document.getElementById('root');
      rootCell.innerHTML = '';
      rootCell.append(element('span', 'k', 'root'), document.createTextNode(root));
      text('crumb-root', root);
    }
    text('slots', info.slots || 1);
    text('cpus', info.cpus || '—');
    var env = document.getElementById('env');
    env.textContent = (info.env_keys || []).length + ' keys';
    env.title = (info.env_keys || []).join(', ');
    if (info.enqueued_at) { enqueuedAt = info.enqueued_at; text('enqueued', clock(enqueuedAt)); }
    if (info.result) {
      startedAt = info.result.started_at || startedAt;
      var outcome = document.getElementById('outcome');
      outcome.innerHTML = '';
      outcome.append(element('span', 'k', 'finished'), document.createTextNode(clock(info.result.finished_at) + ' '), element('span', 'muted', 'took ' + fmtDur(info.result.duration_sec * 1000)), element('span', 'sep', '·'), document.createTextNode('exit ' + info.result.exit_code));
    }
    timeline();
  }
  function info() {
    return getJSON('/api/tasks/' + encodeURIComponent(guid) + '?root=' + encodeURIComponent(root))
      .then(function (data) { render(data); online(true); })
      .catch(function (failure) { online(false, failure); });
  }
  document.getElementById('copy').addEventListener('click', function () {
    if (navigator.clipboard) navigator.clipboard.writeText(guid);
  });
  log.addEventListener('scroll', function () { if (log.scrollTop < 40) older(); });
  var ticks = 0;
  setInterval(function () {
    ticks++;
    timeline();
    if (ticks % 2) return;
    if (state === 'queued') { info().then(tail); return; }
    if (state !== 'done' && state !== 'not_found') info();
  }, 1000);
  info().then(tail).then(function () {
    if (state !== 'queued') setTimeout(tail, 3000);
    log.scrollTop = log.scrollHeight;
  });
})();
</script>
</body>
</html>
`))

// handleTask renders /tasks/<guid>. The first paint comes from one
// control call; everything live happens through /api/tasks/<guid> and
// its /log sibling below.
func (s *webServer) handleTask(w http.ResponseWriter, r *http.Request) {
	guid := strings.TrimPrefix(r.URL.Path, "/tasks/")

	if guid == "" || strings.Contains(guid, "/") {
		http.NotFound(w, r)

		return
	}

	data := pageData{Page: "task", Now: time.Now().UTC().Format(time.RFC3339), Task: TaskInfo{GUID: guid, State: "loading", Root: r.URL.Query().Get("root")}}

	exc := Try(func() {
		var info TaskInfo
		s.getJSON(r.Context(), taskInfoPath(guid, data.Task.Root), &info)
		data.Task = info
	})

	exc.Catch(func(e *Exception) {
		data.Error = e.Error()
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = taskTmpl.Execute(w, data)
}

func taskInfoPath(guid, root string) string {
	return "/v1/tasks/" + url.PathEscape(guid) + "/info?root=" + url.QueryEscape(root)
}

// handleAPITask passes /api/tasks/<guid> and /api/tasks/<guid>/log
// through to control unchanged, query string included; the page cannot
// reach control itself.
func (s *webServer) handleAPITask(w http.ResponseWriter, r *http.Request) {
	exc := Try(func() {
		if r.Method != http.MethodGet {
			ThrowFmt("method not allowed")
		}

		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/", 2)
		guid := parts[0]

		if guid == "" {
			ThrowFmt("guid required")
		}

		path := "/v1/tasks/" + url.PathEscape(guid) + "/info"

		if len(parts) == 2 {
			if parts[1] != "log" {
				ThrowFmt("unknown task resource %q", parts[1])
			}

			path = "/v1/tasks/" + url.PathEscape(guid) + "/log"
		}

		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}

		var raw json.RawMessage
		s.getJSON(r.Context(), path, &raw)
		httpJSON(w, http.StatusOK, raw)
	})

	exc.Catch(func(e *Exception) {
		httpError(w, http.StatusBadGateway, e.Error())
	})
}
