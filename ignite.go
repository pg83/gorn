package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

func igniteMain(args []string) {
	fs := flag.NewFlagSet("ignite", flag.ExitOnError)

	guid := fs.String("guid", "", "task GUID; auto-generated UUIDv4 if empty")
	apiFlag := fs.String("api", "", "gorn control API URL; falls back to $GORN_API")
	wait := fs.Bool("wait", false, "wait for task completion, print stdout/stderr, exit with task exit code")

	var envs stringsFlag
	fs.Var(&envs, "env", "KEY=VALUE (repeatable)")

	Throw(fs.Parse(args))

	cmdArgs := fs.Args()

	if len(cmdArgs) == 0 {
		ThrowFmt("ignite: command is required after flags (use -- to separate)")
	}

	api := resolveAPI(*apiFlag)

	if api == "" {
		ThrowFmt("ignite: --api is required (or set $GORN_API)")
	}

	taskGUID := *guid

	if taskGUID == "" {
		taskGUID = newGUID()
	}

	req := EnqueueReq{GUID: taskGUID, Cmd: cmdArgs, Env: parseEnvs(envs)}
	got := apiEnqueue(api, req)

	if !*wait {
		fmt.Println(got.GUID)

		return
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
	Throw2(rand.Read(b))

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

func apiEnqueue(api string, req EnqueueReq) EnqueueResp {
	body := Throw2(json.Marshal(req))
	target := strings.TrimRight(api, "/") + "/v1/tasks"

	resp := Throw2(igniteHTTP.Post(target, "application/json", bytes.NewReader(body)))
	defer resp.Body.Close()

	data := Throw2(io.ReadAll(resp.Body))

	if resp.StatusCode != http.StatusOK {
		ThrowFmt("ignite: enqueue failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var out EnqueueResp
	Throw(json.Unmarshal(data, &out))

	return out
}

func apiGetState(api, guid string) string {
	target := strings.TrimRight(api, "/") + "/v1/tasks/" + url.PathEscape(guid)
	resp := Throw2(igniteHTTP.Get(target))
	defer resp.Body.Close()

	data := Throw2(io.ReadAll(resp.Body))

	if resp.StatusCode != http.StatusOK {
		ThrowFmt("ignite: state query failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var sr StateResp
	Throw(json.Unmarshal(data, &sr))

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
	resp := Throw2(igniteHTTP.Get(target))
	defer resp.Body.Close()

	data := Throw2(io.ReadAll(resp.Body))

	if resp.StatusCode != http.StatusOK {
		ThrowFmt("ignite: output query failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var out OutputResp
	Throw(json.Unmarshal(data, &out))

	stdout := Throw2(base64.StdEncoding.DecodeString(out.StdoutB64))
	stderr := Throw2(base64.StdEncoding.DecodeString(out.StderrB64))

	Throw2(os.Stdout.Write(stdout))
	Throw2(os.Stderr.Write(stderr))

	var result WrapResult
	Throw(json.Unmarshal(out.Result, &result))

	return result.ExitCode
}
