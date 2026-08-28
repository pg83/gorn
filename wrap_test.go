package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTaskLogRecords(t *testing.T, path string) []wrapTaskLogRecord {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var records []wrapTaskLogRecord
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 2*wrapTaskLogChunk)

	for s.Scan() {
		var rec wrapTaskLogRecord
		if err := json.Unmarshal(s.Bytes(), &rec); err != nil {
			t.Fatalf("decode %q: %v", s.Text(), err)
		}
		records = append(records, rec)
	}

	if err := s.Err(); err != nil {
		t.Fatal(err)
	}

	return records
}

func TestWrapTaskLogWriterFramesLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrap.log")
	log := openWrapLog(path, "task-123")
	out := newWrapTaskLogWriter(log, "fixer", "stdout")

	if _, err := out.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("world\n\nlast")); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	log.close()

	records := readTaskLogRecords(t, path)
	if len(records) != 3 {
		t.Fatalf("records: got %d, want 3", len(records))
	}

	for _, rec := range records {
		if rec.GUID != "task-123" || rec.Root != "fixer" || rec.Stream != "stdout" || rec.TS == "" {
			t.Fatalf("unexpected metadata: %+v", rec)
		}
	}

	got := []string{records[0].Line, records[1].Line, records[2].Line}
	want := []string{"hello world", "", "last"}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWrapTaskLogWriterChunksLongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrap.log")
	log := openWrapLog(path, "task-long")
	errout := newWrapTaskLogWriter(log, "", "stderr")
	want := strings.Repeat("x", wrapTaskLogChunk+17)

	if _, err := errout.Write([]byte(want)); err != nil {
		t.Fatal(err)
	}
	if err := errout.Close(); err != nil {
		t.Fatal(err)
	}
	log.close()

	records := readTaskLogRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("records: got %d, want 2", len(records))
	}
	if !records[0].Partial || records[1].Partial {
		t.Fatalf("unexpected partial markers: %+v", records)
	}
	if records[0].Root != "gorn" || records[0].Stream != "stderr" {
		t.Fatalf("unexpected metadata: %+v", records[0])
	}
	if got := records[0].Line + records[1].Line; got != want {
		t.Fatalf("reassembled line length: got %d, want %d", len(got), len(want))
	}
}
