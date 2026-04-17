package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type WrapInput struct {
	GUID string            `json:"guid"`
	Cmd  []string          `json:"cmd"`
	Env  map[string]string `json:"env,omitempty"`
	User string            `json:"user"`
	S3   S3Config          `json:"s3"`
}

type WrapResult struct {
	GUID        string  `json:"guid"`
	ExitCode    int     `json:"exit_code"`
	StartedAt   string  `json:"started_at"`
	FinishedAt  string  `json:"finished_at"`
	DurationSec float64 `json:"duration_sec"`
	Host        string  `json:"host"`
	User        string  `json:"user"`
}

type FinishMsg struct {
	Type    string `json:"type"`
	GUID    string `json:"guid"`
	Outcome string `json:"outcome"`
	Exit    int    `json:"exit,omitempty"`
}

type cmdResult struct {
	ExitCode   int
	Stdout     []byte
	Stderr     []byte
	StartedAt  time.Time
	FinishedAt time.Time
}

func wrapMain(args []string) {
	if len(args) != 0 {
		ThrowFmt("wrap: takes no arguments, reads context from stdin")
	}

	input := readWrapInput()

	ctx := context.Background()
	cli := newS3Client(input.S3)

	if wrapAlreadyDone(ctx, cli, input.S3.Bucket, input.GUID) {
		emitFinish(FinishMsg{Type: "finish", GUID: input.GUID, Outcome: "already-done"})

		return
	}

	r := runCmd(input)

	uploadResult(ctx, cli, input, r)

	emitFinish(FinishMsg{
		Type:    "finish",
		GUID:    input.GUID,
		Outcome: "completed",
		Exit:    r.ExitCode,
	})
}

func readWrapInput() *WrapInput {
	data := Throw2(io.ReadAll(os.Stdin))

	var in WrapInput
	Throw(json.Unmarshal(data, &in))

	if in.GUID == "" || len(in.Cmd) == 0 || in.User == "" {
		ThrowFmt("wrap: guid, cmd, and user are required in stdin JSON")
	}

	if in.S3.Bucket == "" {
		ThrowFmt("wrap: s3.bucket is required")
	}

	return &in
}

func newS3Client(cfg S3Config) *s3.Client {
	awsCfg := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
	}

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}

		o.UsePathStyle = cfg.UsePathStyle
	})
}

func wrapAlreadyDone(ctx context.Context, cli *s3.Client, bucket, guid string) bool {
	_, err := cli.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(resultKey(guid)),
	})

	if err == nil {
		return true
	}

	if isS3NotFound(err) {
		return false
	}

	Throw(err)

	return false
}

func isS3NotFound(err error) bool {
	var nf *types.NotFound

	if errors.As(err, &nf) {
		return true
	}

	var nsk *types.NoSuchKey

	if errors.As(err, &nsk) {
		return true
	}

	var apiErr smithy.APIError

	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()

		if code == "NotFound" || code == "NoSuchKey" || code == "404" {
			return true
		}
	}

	return false
}

func runCmd(in *WrapInput) cmdResult {
	var stdoutBuf, stderrBuf bytes.Buffer

	cmd := exec.Command(in.Cmd[0], in.Cmd[1:]...)
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.Env = os.Environ()

	for k, v := range in.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	startedAt := time.Now()

	runErr := cmd.Run()

	finishedAt := time.Now()

	exitCode := 0

	var ee *exec.ExitError

	if errors.As(runErr, &ee) {
		exitCode = ee.ExitCode()
	} else if runErr != nil {
		Throw(runErr)
	}

	return cmdResult{
		ExitCode:   exitCode,
		Stdout:     stdoutBuf.Bytes(),
		Stderr:     stderrBuf.Bytes(),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
}

func uploadResult(ctx context.Context, cli *s3.Client, in *WrapInput, r cmdResult) {
	bucket := in.S3.Bucket

	putBytes(ctx, cli, bucket, streamKey(in.GUID, "stdout"), r.Stdout)
	putBytes(ctx, cli, bucket, streamKey(in.GUID, "stderr"), r.Stderr)

	host := Throw2(os.Hostname())

	result := WrapResult{
		GUID:        in.GUID,
		ExitCode:    r.ExitCode,
		StartedAt:   r.StartedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt:  r.FinishedAt.UTC().Format(time.RFC3339Nano),
		DurationSec: r.FinishedAt.Sub(r.StartedAt).Seconds(),
		Host:        host,
		User:        in.User,
	}

	payload := Throw2(json.Marshal(result))

	putBytes(ctx, cli, bucket, resultKey(in.GUID), payload)
}

func putBytes(ctx context.Context, cli *s3.Client, bucket, key string, data []byte) {
	_ = Throw2(cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	}))
}

func emitFinish(msg FinishMsg) {
	data := Throw2(json.Marshal(msg))
	fmt.Println(string(data))
}

func resultKey(guid string) string {
	return "gorn/" + guid + "/result.json"
}

func streamKey(guid, name string) string {
	return "gorn/" + guid + "/" + name
}
