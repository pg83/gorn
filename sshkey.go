package main

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func materializeSSHKeys(endpoints []Endpoint, fallback string) ([]*os.File, func()) {
	files := make([]*os.File, len(endpoints))

	cleanup := func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}

	for i, ep := range endpoints {
		if ep.SSHKey == "" {
			files[i] = Throw2(os.Open(fallback))

			continue
		}

		fd := Throw2(unix.MemfdCreate("gorn-ssh-key", unix.MFD_CLOEXEC))
		f := os.NewFile(uintptr(fd), "gorn-ssh-key")
		body := ep.SSHKey

		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}

		Throw2(f.Write([]byte(body)))
		Throw(f.Chmod(0600))

		files[i] = f
	}

	return files, cleanup
}
