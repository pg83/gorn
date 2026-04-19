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
	descr := fs.String("descr", "", "human-readable task description (shown in web UI); defaults to the joined cmd")
	stdinCmd := fs.Bool("stdin-cmd", false, "read the remote command body from stdin; resulting Cmd is [sh,-c,<stdin>]. Avoids ARG_MAX on large scripts.")

	var envs stringsFlag
	fs.Var(&envs, "env", "KEY=VALUE (repeatable)")

	Throw(fs.Parse(args))

	var cmdArgs []string

	if *stdinCmd {
		if fs.NArg() > 0 {
			ThrowFmt("ignite: --stdin-cmd is mutually exclusive with positional cmd args")
		}

		body := Throw2(io.ReadAll(os.Stdin))
		cmdArgs = []string{"sh", "-c", string(body)}
	} else {
		cmdArgs = fs.Args()

		if len(cmdArgs) == 0 {
			ThrowFmt("ignite: command is required after flags (use -- to separate)")
		}
	}

	api := resolveAPI(*apiFlag)

	if api == "" {
		ThrowFmt("ignite: --api is required (or set $GORN_API)")
	}

	taskGUID := *guid

	if taskGUID == "" {
		taskGUID = newGUID()
	}

	req := EnqueueReq{GUID: taskGUID, Cmd: cmdArgs, Env: parseEnvs(envs), Descr: *descr}
	got, existed := apiEnqueue(api, req)

	if !*wait {
		fmt.Println(got.GUID)

		return
	}

	if existed {
		fmt.Fprintf(os.Stderr, "ignite: task %q already exists in queue; waiting for it\n", got.GUID)
	}

	waitForDone(api, got.GUID)

	exitCode := fetchAndPrintOutput(api, got.GUID)

	os.Exit(exitCode)
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

func apiGetState(api, guid string) string {
	target := strings.TrimRight(api, "/") + "/v1/tasks/" + url.PathEscape(guid)

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

func waitForDone(api, guid string) {
	for {
		state := apiGetState(api, guid)

		if state == "done" {
			return
		}

		if state == "not_found" {
			ThrowFmt("ignite: task %q vanished (neither queued nor done)", guid)
		}

		time.Sleep(500 * time.Millisecond)
	}
}

func fetchAndPrintOutput(api, guid string) int {
	target := strings.TrimRight(api, "/") + "/v1/tasks/" + url.PathEscape(guid) + "/output"

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
