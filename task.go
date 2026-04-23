package main

type Task struct {
	GUID       string            `json:"guid"`
	Script     string            `json:"script"`
	Env        map[string]string `json:"env,omitempty"`
	Descr      string            `json:"descr,omitempty"`
	Root       string            `json:"root,omitempty"`
	Slots      int               `json:"slots,omitempty"`
	EnqueuedAt string            `json:"enqueued_at,omitempty"`
	// RetryOnError, when true, promotes `completed + non-zero exit`
	// from OutcomeNonRetriable to OutcomeRetriable so the leader
	// re-dispatches instead of discarding. Opt-in because the default
	// (non-retriable) is what molot relies on to surface build
	// failures; callers like samogon and ci want the opposite —
	// their ci-check / fetch always indicates success/failure via
	// stream side-channels and any exit != 0 means infra trouble
	// worth retrying.
	RetryOnError bool `json:"retry_on_error,omitempty"`
}
