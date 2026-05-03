package main

type Task struct {
	GUID       string            `json:"guid"`
	Script     string            `json:"script"`
	Env        map[string]string `json:"env,omitempty"`
	Descr      string            `json:"descr,omitempty"`
	Root       string            `json:"root,omitempty"`
	Slots      int               `json:"slots,omitempty"`
	EnqueuedAt string            `json:"enqueued_at,omitempty"`
	// RetryOnError is the single exit code that classifies as retriable
	// (leader leaves the task in queue, wrap skips writing the main
	// result.json so the next dispatch's HEAD-idempotency miss re-runs
	// the script). 0 disables retry. Any other exit code stays
	// non-retriable. Used by molot: the wrap's `molot exec` returns
	// 100 on infra failure (S3 transient, mount failure) vs the
	// script's own exit code on real build failures.
	RetryOnError int `json:"retry_on_error,omitempty"`
}
