package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// execMain decodes a base64-encoded JSON argv and execve's it with PATH
// lookup. Used by `synthesizeScript` so clients can pass positional argv
// through the script pipeline without any shell quoting — the only shell
// on the path sees a static `exec gorn exec <base64>` line where the
// base64 alphabet has no metacharacters.
func execMain(args []string) {
	if len(args) != 1 {
		ThrowFmt("exec: usage: gorn exec <base64-of-json-argv>")
	}

	decoded := Throw2(base64.StdEncoding.DecodeString(args[0]))

	var argv []string
	Throw(json.Unmarshal(decoded, &argv))

	if len(argv) == 0 {
		ThrowFmt("exec: empty argv")
	}

	// When the ignite caller sets `--env PATH=…`, wrap appends that
	// entry to the subprocess envp after its own os.Environ. Linux
	// preserves duplicates in envp, but Go's os.Getenv returns the
	// FIRST match — so the ssh-login PATH would beat whatever the
	// caller passed, and LookPath would search the wrong places.
	// Walk envp last-wins and Setenv so LookPath honors the PATH
	// the caller actually specified.
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "PATH=") {
			Throw(os.Setenv("PATH", e[len("PATH="):]))
		}
	}

	path := Throw2(exec.LookPath(argv[0]))

	Throw(syscall.Exec(path, argv, os.Environ()))
}
