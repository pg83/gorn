package main

type Task struct {
	GUID       string            `json:"guid"`
	Cmd        []string          `json:"cmd"`
	Env        map[string]string `json:"env,omitempty"`
	Descr      string            `json:"descr,omitempty"`
	Root       string            `json:"root,omitempty"`
	Slots      int               `json:"slots,omitempty"`
	EnqueuedAt string            `json:"enqueued_at,omitempty"`
}
