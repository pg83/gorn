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

func TestListenPortTakesPortFromAddr(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:7879":   "7879",
		":7879":          "7879",
		"127.0.0.1:7879": "7879",
		"":               "",
		"7879":           "",
	}

	for addr, want := range cases {
		if got := listenPort(addr); got != want {
			t.Errorf("listenPort(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestDispatcherInflightIsACopy(t *testing.T) {
	d := &Dispatcher{inflight: map[string]string{"guid-1": "worker-1"}}

	got := d.Inflight()

	if got["guid-1"] != "worker-1" {
		t.Fatalf("Inflight() = %v, want guid-1 on worker-1", got)
	}

	// Mutating the snapshot must not reach into the dispatcher's own map,
	// which the dispatch loop keeps writing under its mutex.
	got["guid-1"] = "tampered"
	delete(got, "guid-1")

	if d.inflight["guid-1"] != "worker-1" {
		t.Errorf("Inflight() handed out the live map: %v", d.inflight)
	}
}

func TestListenHostOnlyWhenConcrete(t *testing.T) {
	cases := map[string]string{
		"192.168.103.16:8029": "192.168.103.16",
		"lab1:8029":           "lab1",
		"0.0.0.0:8029":        "",
		":8029":               "",
		"[::]:8029":           "",
		"":                    "",
	}

	for addr, want := range cases {
		if got := listenHost(addr); got != want {
			t.Errorf("listenHost(%q) = %q, want %q", addr, got, want)
		}
	}
}
