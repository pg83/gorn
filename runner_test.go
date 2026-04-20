package main

import "testing"

func TestLastFinishMsg_Completed(t *testing.T) {
	stdout := `{"type":"log","line":"hello"}
{"type":"finish","guid":"abc","outcome":"completed","exit":0}
`
	msg := lastFinishMsg(stdout)

	if msg == nil {
		t.Fatal("expected finish msg, got nil")
	}

	if msg.GUID != "abc" || msg.Outcome != "completed" || msg.Exit != 0 {
		t.Errorf("unexpected msg: %+v", msg)
	}
}

func TestLastFinishMsg_AlreadyDone(t *testing.T) {
	stdout := `{"type":"finish","guid":"x","outcome":"already-done"}`
	msg := lastFinishMsg(stdout)

	if msg == nil || msg.Outcome != "already-done" {
		t.Fatalf("unexpected: %+v", msg)
	}
}

func TestLastFinishMsg_IgnoresTrailingGarbage(t *testing.T) {
	stdout := `{"type":"finish","guid":"x","outcome":"completed","exit":2}
some garbage line
not json either
`
	msg := lastFinishMsg(stdout)

	if msg == nil || msg.Exit != 2 {
		t.Fatalf("unexpected: %+v", msg)
	}
}

func TestLastFinishMsg_NoFinish(t *testing.T) {
	stdout := `{"type":"log","line":"hello"}` + "\n"
	msg := lastFinishMsg(stdout)

	if msg != nil {
		t.Fatalf("expected nil, got: %+v", msg)
	}
}

func TestLastFinishMsg_Empty(t *testing.T) {
	if lastFinishMsg("") != nil {
		t.Fatal("expected nil for empty input")
	}
}

func TestClassify_Success(t *testing.T) {
	stdout := `{"type":"finish","guid":"g","outcome":"completed","exit":0}`
	out, _ := classify(stdout, "")

	if out != OutcomeSuccess {
		t.Errorf("got %v, want success", out)
	}
}

func TestClassify_NonRetriable(t *testing.T) {
	stdout := `{"type":"finish","guid":"g","outcome":"completed","exit":7}`
	out, detail := classify(stdout, "")

	if out != OutcomeNonRetriable {
		t.Errorf("got %v, want non-retriable", out)
	}

	if detail != "exit 7" {
		t.Errorf("got detail %q, want %q", detail, "exit 7")
	}
}

func TestClassify_AlreadyDone(t *testing.T) {
	stdout := `{"type":"finish","guid":"g","outcome":"already-done"}`
	out, _ := classify(stdout, "")

	if out != OutcomeSuccess {
		t.Errorf("got %v, want success", out)
	}
}

func TestClassify_Retriable_NoFinish(t *testing.T) {
	out, _ := classify("", "permission denied")

	if out != OutcomeRetriable {
		t.Errorf("got %v, want retriable", out)
	}
}

func TestClassify_Retriable_UnknownOutcome(t *testing.T) {
	stdout := `{"type":"finish","guid":"g","outcome":"weird"}`
	out, _ := classify(stdout, "")

	if out != OutcomeRetriable {
		t.Errorf("got %v, want retriable", out)
	}
}

