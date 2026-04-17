package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
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

func runTaskOnEndpoint(ctx context.Context, ep Endpoint, task Task, s3cfg S3Config, sshKey []byte) (outcome RunOutcome, detail string) {
	outcome = OutcomeRetriable
	detail = ""

	exc := Try(func() {
		outcome, detail = doRunTask(ctx, ep, task, s3cfg, sshKey)
	})

	exc.Catch(func(e *Exception) {
		outcome = OutcomeRetriable
		detail = "transport: " + e.Error()
	})

	return outcome, detail
}

func doRunTask(ctx context.Context, ep Endpoint, task Task, s3cfg S3Config, sshKey []byte) (RunOutcome, string) {
	input := WrapInput{
		GUID: task.GUID,
		Cmd:  task.Cmd,
		Env:  task.Env,
		User: ep.User,
		S3:   s3cfg,
	}

	inputJSON := Throw2(json.Marshal(input))

	signer := Throw2(ssh.ParsePrivateKey(sshKey))

	config := &ssh.ClientConfig{
		User:            ep.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	addr := ep.Host

	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "22")
	}

	client := Throw2(ssh.Dial("tcp", addr, config))
	defer client.Close()

	session := Throw2(client.NewSession())
	defer session.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	session.Stdout = &stdoutBuf
	session.Stderr = &stderrBuf
	session.Stdin = bytes.NewReader(inputJSON)

	remoteCmd := fmt.Sprintf("cd %s && gorn wrap", shellQuote(ep.Path))

	runErr := session.Run(remoteCmd)

	_ = runErr

	outcome, detail := classify(stdoutBuf.String(), stderrBuf.String())

	return outcome, detail
}

func classify(stdout, stderr string) (RunOutcome, string) {
	finish := lastFinishMsg(stdout)

	if finish == nil {
		return OutcomeRetriable, "no finish message; stderr: " + truncate(stderr, 400)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "...(truncated)"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
