package main

import "testing"

func TestParseStatusUID(t *testing.T) {
	status := []byte(`Name:	sleep
Umask:	0022
State:	S (sleeping)
Tgid:	1234
Uid:	1001	1001	1001	1001
Gid:	1001	1001	1001	1001
`)

	got := parseStatusUID(status)

	if got != 1001 {
		t.Errorf("got %d, want 1001", got)
	}
}

func TestParseStatusUID_Missing(t *testing.T) {
	exc := Try(func() {
		parseStatusUID([]byte("Name:	x\n"))
	})

	if exc == nil {
		t.Fatal("expected error when Uid: line missing")
	}
}
