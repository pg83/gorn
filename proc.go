package main

import (
	"fmt"
	"os"
	osuser "os/user"
	"strconv"
	"strings"
	"syscall"
)

func killStaleUnder(username string) {
	self := os.Getpid()
	parent := os.Getppid()

	u := Throw2(osuser.Lookup(username))
	targetUID := Throw2(strconv.Atoi(u.Uid))

	if targetUID == 0 {
		fmt.Fprintln(os.Stderr, "wrap: refusing to kill stale processes under uid 0 (test mode)")

		return
	}

	entries := Throw2(os.ReadDir("/proc"))

	for _, e := range entries {
		pid, parseErr := strconv.Atoi(e.Name())

		if parseErr != nil {
			continue
		}

		if pid == self || pid == parent {
			continue
		}

		_ = Try(func() {
			killIfOwnedByUID(pid, targetUID)
		})
	}
}

func killIfOwnedByUID(pid, targetUID int) {
	data := Throw2(os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)))

	uid := parseStatusUID(data)

	if uid == targetUID {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func parseStatusUID(data []byte) int {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}

		fields := strings.Fields(line)

		if len(fields) < 2 {
			ThrowFmt("malformed Uid: line")
		}

		return Throw2(strconv.Atoi(fields[1]))
	}

	ThrowFmt("Uid: line not found")

	return 0
}
