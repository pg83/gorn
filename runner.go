package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
		GUID: task.GUID,
		Cmd:  task.Cmd,
		Env:  task.Env,
		User: ep.User,
		S3:   s3cfg,
	}

	inputJSON := Throw2(json.Marshal(input))

	payload := fmt.Sprintf("cd %s && PATH=$PWD:$PATH gorn wrap", shellQuote(ep.Path))
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	remoteCmd := `eval "$(echo ` + encoded + ` | base64 -d)"`

	port := ep.Port

	if port == 0 {
		port = 22
	}

	sshArgs := []string{
		"-i", sshKeyFdPath,
		"-p", strconv.Itoa(port),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30",
		ep.User + "@" + ep.Host,
		remoteCmd,
	}

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.ExtraFiles = []*os.File{keyFile}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	_ = cmd.Run()

	outcome, detail := classify(stdoutBuf.String(), stderrBuf.String())

	return outcome, detail
}

func classify(stdout, stderr string) (RunOutcome, string) {
	finish := lastFinishMsg(stdout)

	if finish == nil {
		return OutcomeRetriable, "no finish message; stderr: " + stderr
	}

	if finish.Outcome == "already-done" {
		return OutcomeSuccess, "already-done"
	}

	if finish.Outcome == "completed" {
		if finish.Exit == 0 {
			return OutcomeSuccess, "exit 0"
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
