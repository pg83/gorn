package main

type Task struct {
	GUID       string            `json:"guid"`
	Script     string            `json:"script"`
	Env        map[string]string `json:"env,omitempty"`
	Descr      string            `json:"descr,omitempty"`
	Root       string            `json:"root,omitempty"`
	Slots      int               `json:"slots,omitempty"`
	EnqueuedAt string            `json:"enqueued_at,omitempty"`
	// RetryOnError, when non-nil, names the single exit code that
	// classifies as retriable (leader leaves the task in queue, wrap
	// skips writing the main result.json so the next dispatch's
	// HEAD-idempotency miss re-runs the script). nil disables retry —
	// any non-zero exit stays non-retriable. Pointer-not-zero so that
	// "exit 0 should retry" is a representable thing if anyone ever
	// needs it; the previous int+0-sentinel form conflated "not set"
	// with "retry on 0". Used by molot: `molot exec` returns 100 on
	// infra failure (S3 transient, mount failure) vs the script's
	// own exit code on real build failures.
	RetryOnError *int `json:"retry_on_error,omitempty"`
}
