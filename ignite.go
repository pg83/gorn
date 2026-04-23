package main

import (
	"bytes"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type stringsFlag []string

func (s *stringsFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringsFlag) Set(v string) error {
	*s = append(*s, v)

	return nil
}

var igniteHTTP = &http.Client{Timeout: 30 * time.Second}

// retry runs fn with exp backoff + jitter. fn returns (done, err):
//   - done=true   → success, stop
//   - done=false  → transient, retry
// err with done=true is a hard error and is returned to the caller.
func retry(label string, fn func() (done bool, err error)) error {
	const maxDelay = 60 * time.Second
	delay := time.Second

	for {
		done, err := fn()

		if done {
			return err
		}

		sleep := delay/2 + time.Duration(rand.Int64N(int64(delay)))
		fmt.Fprintf(os.Stderr, "ignite: %s: transient error (%v), retrying in %v\n", label, err, sleep)
		time.Sleep(sleep)

		delay *= 2

		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// isTransientStatus treats 5xx and 429 as transient.
func isTransientStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func igniteMain(args []string) {
	fs := flag.NewFlagSet("ignite", flag.ExitOnError)

	guid := fs.String("guid", "", "task GUID; auto-generated UUIDv4 if empty")
	apiFlag := fs.String("api", "", "gorn control API URL; falls back to $GORN_API")
	wait := fs.Bool("wait", false, "wait for task completion, print stdout/stderr, exit with task exit code")
	descr := fs.String("descr", "", "human-readable task description (shown in web UI); defaults to first non-empty line of the script")
	root := fs.String("root", "cli", "S3 key prefix for this task's artifacts (<root>/<guid>/...)")
	slots := fs.Int("slots", 0, "number of host slots this task requires; default 1, rejected if larger than any host's slot count")
	retryOnError := fs.Bool("retry-error", false, "promote completed+non-zero exits from non-retriable to retriable so the leader re-dispatches (opt-in; molot relies on default non-retriable)")

	var envs stringsFlag
	fs.Var(&envs, "env", "KEY=VALUE (repeatable)")

	Throw(fs.Parse(args))

	// Script body: positional args after '--' synthesize one (compat mode
	// for old 'gorn ignite ... -- argv0 argv1 ...' callers); otherwise
	// the script is read from stdin. Inside gorn everything is a script
	// — the worker writes it to a memfd and execs it directly, so there
	// is no ARG_MAX limit on the body.
	//
	// In '-- argv' mode, if the client's own stdin is a pipe/file (not a
	// TTY), it's read and embedded into the script so the worker pipes
	// those bytes into the inner cmd's stdin. This makes
	// `cat foo.bin | gorn ignite -- cmd` work end-to-end: the bytes travel
	// as base64 inside the script, survive ARG_MAX, and land on cmd's
	// stdin on the worker.
	var script string

	if fs.NArg() > 0 {
		var stdin []byte

		// Don't try to read from a TTY — it would block the client on
		// keyboard input nobody's providing. A TTY means "no pipe",
		// which is the same thing as an empty pipe for the inner cmd:
		// immediate EOF on stdin.
		if !stdinIsTTY() {
			stdin = Throw2(io.ReadAll(os.Stdin))
		}

		script = synthesizeScript(fs.Args(), stdin)

		if *descr == "" {
			*descr = strings.Join(fs.Args(), " ")
		}
	} else {
		body := Throw2(io.ReadAll(os.Stdin))

		if len(body) == 0 {
			ThrowFmt("ignite: empty script on stdin (pass '-- argv...' or pipe a script)")
		}

		script = string(body)
	}

	api := resolveAPI(*apiFlag)

	if api == "" {
		ThrowFmt("ignite: --api is required (or set $GORN_API)")
	}

	taskGUID := *guid

	if taskGUID == "" {
		taskGUID = newGUID()
	}

	req := EnqueueReq{GUID: taskGUID, Script: script, Env: parseEnvs(envs), Descr: *descr, Root: *root, Slots: *slots, RetryOnError: *retryOnError}
	got, existed := apiEnqueue(api, req)

	if !*wait {
		fmt.Println(got.GUID)

		return
	}

	if existed {
		fmt.Fprintf(os.Stderr, "ignite: task %q already exists in queue; waiting for it\n", got.GUID)
	}

	waitForDone(api, got.GUID, *root)

	exitCode := fetchAndPrintOutput(api, got.GUID, *root)

	os.Exit(exitCode)
}

// synthesizeScript turns a positional cmdline (`ignite ... -- foo bar baz`)
// into a minimal shebang'd script so the server-side script model stays
// the single execution path. argv travels as base64-encoded JSON and
// stdin as a base64 heredoc piped into the inner cmd — no shell quoting,
// no ARG_MAX, no surprises. The heredoc marker uses underscores, which
// aren't in the base64 alphabet, so it can't collide with the payload.
func synthesizeScript(argv []string, stdin []byte) string {
	argvB64 := base64.StdEncoding.EncodeToString(Throw2(json.Marshal(argv)))
	stdinB64 := base64.StdEncoding.EncodeToString(stdin)

	return "#!/bin/sh\nbase64 -d <<'__GORN_STDIN_B64__' | gorn exec " + argvB64 + "\n" + stdinB64 + "\n__GORN_STDIN_B64__\n"
}

func stdinIsTTY() bool {
	st, err := os.Stdin.Stat()

	if err != nil {
		return false
	}

	return (st.Mode() & os.ModeCharDevice) != 0
}

func resolveAPI(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}

	return os.Getenv("GORN_API")
}

func newGUID() string {
	b := make([]byte, 16)
	Throw2(crand.Read(b))

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func parseEnvs(envs []string) map[string]string {
	if len(envs) == 0 {
		return nil
	}

	out := make(map[string]string, len(envs))

	for _, e := range envs {
		idx := strings.IndexByte(e, '=')

		if idx <= 0 {
			ThrowFmt("ignite: --env %q must be KEY=VALUE with non-empty KEY", e)
		}

		out[e[:idx]] = e[idx+1:]
	}

	return out
}

func apiEnqueue(api string, req EnqueueReq) (EnqueueResp, bool) {
	body := Throw2(json.Marshal(req))
	target := strings.TrimRight(api, "/") + "/v1/tasks"

	var out EnqueueResp
	var existed bool

	Throw(retry("enqueue", func() (bool, error) {
		resp, err := igniteHTTP.Post(target, "application/json", bytes.NewReader(body))

		if err != nil {
			return false, err
		}

		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)

		if err != nil {
			return false, err
		}

		if resp.StatusCode == http.StatusConflict {
			out = EnqueueResp{GUID: req.GUID}
			existed = true

			return true, nil
		}

		if isTransientStatus(resp.StatusCode) {
			return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}

		if resp.StatusCode != http.StatusOK {
			return true, fmt.Errorf("enqueue failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}

		if err := json.Unmarshal(data, &out); err != nil {
			return true, err
		}

		return true, nil
	}))

	return out, existed
}

func apiGetState(api, guid, root string) string {
	target := strings.TrimRight(api, "/") + "/v1/tasks/" + url.PathEscape(guid)

	if root != "" {
		target += "?root=" + url.QueryEscape(root)
	}

	var sr StateResp

	Throw(retry("state", func() (bool, error) {
		resp, err := igniteHTTP.Get(target)

		if err != nil {
			return false, err
		}

		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)

		if err != nil {
			return false, err
		}

		if isTransientStatus(resp.StatusCode) {
			return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}

		if resp.StatusCode != http.StatusOK {
			return true, fmt.Errorf("state query failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}

		if err := json.Unmarshal(data, &sr); err != nil {
			return true, err
		}

		return true, nil
	}))

	return sr.State
}

func waitForDone(api, guid, root string) {
	// Start polling tight (500ms) so short-running tasks don't drag,
	// then back off to a 10s ceiling — most waits sit behind long
	// molot builds, and 2 polls/s × N waiting ignites was measurable
	// load on control/etcd. tout*1.3 + sleep = tout/2 + rand(0, tout)
	// gives a smooth ramp plus per-waiter jitter so many concurrent
	// waiters don't hit control in lock-step.
	tout := 500 * time.Millisecond
	const toutCap = 10 * time.Second

	for {
		state := apiGetState(api, guid, root)

		if state == "done" {
			return
		}

		if state == "not_found" {
			ThrowFmt("ignite: task %q vanished (neither queued nor done)", guid)
		}

		sleep := tout/2 + time.Duration(rand.Int64N(int64(tout)))
		time.Sleep(sleep)

		tout = time.Duration(float64(tout) * 1.3)

		if tout > toutCap {
			tout = toutCap
		}
	}
}

func fetchAndPrintOutput(api, guid, root string) int {
	target := strings.TrimRight(api, "/") + "/v1/tasks/" + url.PathEscape(guid) + "/output"

	if root != "" {
		target += "?root=" + url.QueryEscape(root)
	}

	var out OutputResp

	Throw(retry("output", func() (bool, error) {
		resp, err := igniteHTTP.Get(target)

		if err != nil {
			return false, err
		}

		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)

		if err != nil {
			return false, err
		}

		if isTransientStatus(resp.StatusCode) {
			return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}

		if resp.StatusCode != http.StatusOK {
			return true, fmt.Errorf("output query failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}

		if err := json.Unmarshal(data, &out); err != nil {
			return true, err
		}

		return true, nil
	}))

	stdout := Throw2(base64.StdEncoding.DecodeString(out.StdoutB64))
	stderr := Throw2(base64.StdEncoding.DecodeString(out.StderrB64))

	Throw2(os.Stdout.Write(stdout))
	Throw2(os.Stderr.Write(stderr))

	var result WrapResult
	Throw(json.Unmarshal(out.Result, &result))

	return result.ExitCode
}
