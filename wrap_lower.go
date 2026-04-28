package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// wrapLowerMain runs inside the user+mount namespace that `gorn wrap`
// opens for a task. Its whole job is to give the task a clean tmpfs
// as its cwd, then execve the task script.
//
// The dance matters because wrap chdir'd into the endpoint path
// before the fork+exec+unshare chain, so our cwd fd is inherited
// pinned to the pre-mount inode. Covering the path with a tmpfs by
// name alone leaves that pinned fd intact — the task's relative
// lookups still hit the old (hidden) inode while absolute lookups
// go through the mount. Re-chdir'ing by absolute name after the
// mount re-opens the path through the kernel, landing us on the
// new tmpfs root; from here on relative and absolute views agree.
//
// The script path is always `/proc/self/fd/N` for the memfd wrap
// created (no MFD_CLOEXEC), so it survives the whole fork+exec
// chain into us.
func wrapLowerMain(args []string) {
	if len(args) != 2 {
		ThrowFmt("wrap_lower: usage: gorn wrap_lower <cwd> <script-path>")
	}

	cwd := args[0]
	scriptPath := args[1]

	Throw(syscall.Mount("tmpfs", cwd, "tmpfs", 0, ""))
	Throw(syscall.Chdir(cwd))
	Throw(unix.Setpriority(unix.PRIO_PROCESS, 0, 19))
	Throw(syscall.Exec(scriptPath, []string{scriptPath}, os.Environ()))
}
