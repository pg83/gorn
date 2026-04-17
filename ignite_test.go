package main

import "testing"

func TestParseEnvs_Empty(t *testing.T) {
	got := parseEnvs(nil)

	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestParseEnvs_Basic(t *testing.T) {
	got := parseEnvs([]string{"FOO=bar", "BAZ=qux"})

	if got["FOO"] != "bar" || got["BAZ"] != "qux" || len(got) != 2 {
		t.Errorf("unexpected: %v", got)
	}
}

func TestParseEnvs_ValueWithEquals(t *testing.T) {
	got := parseEnvs([]string{"URL=http://x?a=b&c=d"})

	if got["URL"] != "http://x?a=b&c=d" {
		t.Errorf("unexpected: %v", got)
	}
}

func TestParseEnvs_MissingEquals(t *testing.T) {
	exc := Try(func() {
		parseEnvs([]string{"FOO"})
	})

	if exc == nil {
		t.Fatal("expected error for missing =")
	}
}

func TestParseEnvs_EmptyKey(t *testing.T) {
	exc := Try(func() {
		parseEnvs([]string{"=bar"})
	})

	if exc == nil {
		t.Fatal("expected error for empty key")
	}
}
