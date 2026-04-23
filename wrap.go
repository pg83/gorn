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
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"golang.org/x/sys/unix"
)

type WrapInput struct {
	GUID    string            `json:"guid"`
	Script  string            `json:"script"`
	Env     map[string]string `json:"env,omitempty"`
	User    string            `json:"user"`
	Root    string            `json:"root,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	S3      S3Config          `json:"s3"`
	LogPath string            `json:"log_path,omitempty"`
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
	if len(args) > 1 {
		ThrowFmt("wrap: takes at most one positional (guid, cosmetic for ps)")
	}

	input := readWrapInput()

	if input.Cwd == "" {
		ThrowFmt("wrap: input.Cwd is required (endpoint.path must be set)")
	}

	Throw(os.Chdir(input.Cwd))
	Throw(os.Setenv("PATH", input.Cwd+":"+os.Getenv("PATH")))

	log := openWrapLog(input.LogPath, input.GUID)
	defer log.close()

	exc := Try(func() {
		wrapBody(input, log)
	})

	if exc != nil {
		log.logf("wrap failed: %s", exc.Error())
		panic(exc)
	}
}

func wrapBody(input *WrapInput, log *wrapLog) {
	host := Throw2(os.Hostname())
	log.logf("wrap start: host=%s user=%s script_bytes=%d env_keys=%v s3=%s/%s", host, input.User, len(input.Script), sortedKeys(input.Env), input.S3.Endpoint, input.S3.Bucket)

	ctx := context.Background()

	t := time.Now()
	cli := newS3Client(input.S3)
	log.logf("s3 client ready took=%.3fs", time.Since(t).Seconds())

	t = time.Now()
	already := wrapAlreadyDone(ctx, cli, input.S3.Bucket, input.Root, input.GUID)
	log.logf("s3 head (idempotency) took=%.3fs already_done=%v", time.Since(t).Seconds(), already)

	if already {
		log.logf("idempotency hit: result.json already in s3, emitting already-done")
		emitFinish(FinishMsg{Type: "finish", GUID: input.GUID, Outcome: "already-done"})

		return
	}

	log.logf("running command")
	r := runCmd(input)
	log.logf("command finished: exit=%d duration=%.3fs stdout_len=%d stderr_len=%d", r.ExitCode, r.FinishedAt.Sub(r.StartedAt).Seconds(), len(r.Stdout), len(r.Stderr))

	log.logf("uploading to s3 bucket=%s prefix=gorn/%s/", input.S3.Bucket, input.GUID)
	uploadResult(ctx, cli, input, r, log)
	log.logf("upload done")

	emitFinish(FinishMsg{
		Type:    "finish",
		GUID:    input.GUID,
		Outcome: "completed",
		Exit:    r.ExitCode,
	})

	log.logf("finish emitted: outcome=completed exit=%d", r.ExitCode)
}

type wrapLog struct {
	f    *os.File
	guid string
}

func openWrapLog(path, guid string) *wrapLog {
	if path == "" {
		return &wrapLog{guid: guid}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)

	if err != nil {
		uid, gid := os.Getuid(), os.Getgid()
		wd, _ := os.Getwd()
		fmt.Fprintf(os.Stderr, "wrap: log open failed: path=%q uid=%d gid=%d cwd=%q err=%v\n", path, uid, gid, wd, err)

		return &wrapLog{guid: guid}
	}

	return &wrapLog{f: f, guid: guid}
}

func (l *wrapLog) logf(format string, args ...any) {
	if l == nil || l.f == nil {
		return
	}

	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] guid=%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), l.guid, msg)

	_, _ = l.f.WriteString(line)
}

func (l *wrapLog) close() {
	if l != nil && l.f != nil {
		_ = l.f.Close()
	}
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}

	keys := make([]string, 0, len(m))

	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

func readWrapInput() *WrapInput {
	data := Throw2(io.ReadAll(os.Stdin))

	var in WrapInput
	Throw(json.Unmarshal(data, &in))

	if in.GUID == "" || in.Script == "" || in.User == "" {
		ThrowFmt("wrap: guid, script, and user are required in stdin JSON")
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

func wrapAlreadyDone(ctx context.Context, cli *s3.Client, bucket, root, guid string) bool {
	_, err := cli.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(resultKey(root, guid)),
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

// runCmd writes the task's script into an anonymous in-memory file
// (memfd_create) and execs it via /proc/self/fd/N. This sidesteps
// ARG_MAX (scripts can be hundreds of KB once a big graph's worth of
// dep paths is baked in) because the body never rides on the argv.
// The kernel's binfmt_script handler reads the shebang off the memfd
// and spawns the interpreter, which then reads the script off the
// same inherited fd via /proc/self/fd/N.
func runCmd(in *WrapInput) cmdResult {
	var stdoutBuf, stderrBuf bytes.Buffer

	// Don't set MFD_CLOEXEC: the fd must survive fork+exec so the child
	// (the script interpreter) can see the body via /proc/self/fd/N.
	fd, err := unix.MemfdCreate("gorn-script", 0)
	Throw(err)

	defer unix.Close(fd)

	_, err = unix.Write(fd, []byte(in.Script))
	Throw(err)

	path := fmt.Sprintf("/proc/self/fd/%d", fd)

	// Put the task's cwd behind a per-run tmpfs inside its own user+mount
	// namespace. When wrap exits the ns dies with it — tmpfs unmounts,
	// scratch contents disappear, no orphan mkdirs/clones between
	// invocations. Downstream tools (molot, ci check, samogon fetch)
	// can litter cwd freely.
	//
	// `-r -U -m` gives us CAP_SYS_ADMIN in the new user ns so the
	// tmpfs mount works from an unprivileged endpoint user (gorn_N).
	// `gorn wrap_lower` runs inside the fresh ns and handles the
	// mount + chdir + execve in Go directly — no shell quoting, no
	// path string concat. See wrap_lower.go for why the re-chdir is
	// load-bearing.
	//
	// The memfd fd is inherited across the fork+exec chain (no
	// MFD_CLOEXEC above), so /proc/self/fd/N still resolves in
	// wrap_lower and the final script.
	self := Throw2(os.Executable())
	cmd := exec.Command("/bin/unshare", "-r", "-U", "-m",
		self, "wrap_lower", in.Cwd, path)
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

func uploadResult(ctx context.Context, cli *s3.Client, in *WrapInput, r cmdResult, log *wrapLog) {
	bucket := in.S3.Bucket

	putBytes(ctx, cli, bucket, streamKey(in.Root, in.GUID, "stdout"), r.Stdout, log)
	putBytes(ctx, cli, bucket, streamKey(in.Root, in.GUID, "stderr"), r.Stderr, log)

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

	putBytes(ctx, cli, bucket, resultKey(in.Root, in.GUID), payload, log)
}

func putBytes(ctx context.Context, cli *s3.Client, bucket, key string, data []byte, log *wrapLog) {
	t := time.Now()
	_ = Throw2(cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	}))

	log.logf("s3 put key=%s size=%d took=%.3fs", key, len(data), time.Since(t).Seconds())
}

func emitFinish(msg FinishMsg) {
	data := Throw2(json.Marshal(msg))
	fmt.Println(string(data))
}

func rootOr(root string) string {
	if root == "" {
		return "gorn"
	}

	return root
}

func resultKey(root, guid string) string {
	return rootOr(root) + "/" + guid + "/result.json"
}

func streamKey(root, guid, name string) string {
	return rootOr(root) + "/" + guid + "/" + name
}
