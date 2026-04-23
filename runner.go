package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type RunOutcome int

const (
	OutcomeSuccess RunOutcome = iota
	OutcomeNonRetriable
	OutcomeRetriable
)

func (o RunOutcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeNonRetriable:
		return "non-retriable"
	case OutcomeRetriable:
		return "retriable"
	}

	return "unknown"
}

func runTaskOnEndpoint(ctx context.Context, ep Endpoint, task Task, s3cfg S3Config, keyFile *os.File) (outcome RunOutcome, detail string) {
	outcome = OutcomeRetriable
	detail = ""

	exc := Try(func() {
		outcome, detail = doRunTask(ctx, ep, task, s3cfg, keyFile)
	})

	exc.Catch(func(e *Exception) {
		outcome = OutcomeRetriable
		detail = "transport: " + e.Error()
	})

	return outcome, detail
}

func doRunTask(ctx context.Context, ep Endpoint, task Task, s3cfg S3Config, keyFile *os.File) (RunOutcome, string) {
	input := WrapInput{
		GUID:    task.GUID,
		Script:  task.Script,
		Env:     task.Env,
		User:    ep.User,
		Root:    task.Root,
		Cwd:     ep.Path,
		S3:      s3cfg,
		LogPath: ep.LogPath,
	}

	inputJSON := Throw2(json.Marshal(input))

	port := ep.Port

	if port == 0 {
		port = 22
	}

	keyPath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), keyFile.Fd())

	sshArgs := []string{
		"-i", keyPath,
		"-p", strconv.Itoa(port),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30",
		ep.User + "@" + ep.Host,
		"gorn", "wrap", task.GUID,
	}

	cmd := exec.CommandContext(ctx, resolveSSH(), sshArgs...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	fmt.Fprintf(os.Stderr, "dispatch: task=%s ep=%s@%s:%d log_path=%q script_bytes=%d ssh_args=%v\n", task.GUID, ep.User, ep.Host, port, ep.LogPath, len(task.Script), sshArgs)

	runErr := cmd.Run()

	exitCode := -1

	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	fmt.Fprintf(os.Stderr, "dispatch: task=%s ep=%s@%s ssh_exit=%d run_err=%v stdout_len=%d stderr_len=%d\n", task.GUID, ep.User, ep.Host, exitCode, runErr, stdoutBuf.Len(), stderrBuf.Len())

	if stderrBuf.Len() > 0 {
		fmt.Fprintf(os.Stderr, "dispatch: task=%s ep=%s@%s stderr:\n%s", task.GUID, ep.User, ep.Host, stderrBuf.String())
	}

	outcome, detail := classify(stdoutBuf.String(), stderrBuf.String(), task.RetryOnError)

	return outcome, detail
}

func classify(stdout, stderr string, retryOnError bool) (RunOutcome, string) {
	finish := lastFinishMsg(stdout)

	if finish == nil {
		return OutcomeRetriable, fmt.Sprintf("no finish message; stdout=%q stderr=%q", stdout, stderr)
	}

	if finish.Outcome == "already-done" {
		return OutcomeSuccess, "already-done"
	}

	if finish.Outcome == "completed" {
		if finish.Exit == 0 {
			return OutcomeSuccess, "exit 0"
		}

		if retryOnError {
			return OutcomeRetriable, fmt.Sprintf("exit %d (retry-error)", finish.Exit)
		}

		return OutcomeNonRetriable, fmt.Sprintf("exit %d", finish.Exit)
	}

	return OutcomeRetriable, "unknown outcome: " + finish.Outcome
}

func lastFinishMsg(stdout string) *FinishMsg {
	lines := strings.Split(stdout, "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])

		if line == "" {
			continue
		}

		var msg FinishMsg
		err := json.Unmarshal([]byte(line), &msg)

		if err != nil {
			continue
		}

		if msg.Type == "finish" {
			return &msg
		}
	}

	return nil
}

func resolveSSH() string {
	p, err := exec.LookPath("ssh")

	if err == nil {
		return p
	}

	for _, cand := range []string{"/usr/bin/ssh", "/bin/ssh", "/usr/local/bin/ssh"} {
		if _, statErr := os.Stat(cand); statErr == nil {
			return cand
		}
	}

	ThrowFmt("ssh: executable not found in PATH or /usr/bin, /bin, /usr/local/bin (lookup err: %v)", err)

	return ""
}
