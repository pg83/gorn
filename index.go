package main

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// QueueIndex mirrors the /gorn/queue/ prefix in memory.
// Initial snapshot + live Watch; one writer goroutine (Run), many readers.
// Survives Watch compaction by resyncing and restarting the Watch.
type QueueIndex struct {
	cli *clientv3.Client

	mu    sync.RWMutex
	byKey map[string]QueueItem

	// wake is signalled on every committed change. Host loops select on
	// it to re-scan; drained lazily, so never blocks.
	wake chan struct{}
}

func NewQueueIndex(cli *clientv3.Client) *QueueIndex {
	return &QueueIndex{
		cli:   cli,
		byKey: map[string]QueueItem{},
		wake:  make(chan struct{}, 1),
	}
}

// Wake returns a channel fired on any queue change.
func (i *QueueIndex) Wake() <-chan struct{} {
	return i.wake
}

func (i *QueueIndex) signal() {
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

// Snapshot returns a sorted (by CreateRevision asc) copy of the current queue.
// Copy is cheap — tasks are value types with slice/map fields that are
// never mutated after upsert.
func (i *QueueIndex) Snapshot() []QueueItem {
	i.mu.RLock()
	defer i.mu.RUnlock()

	out := make([]QueueItem, 0, len(i.byKey))

	for _, it := range i.byKey {
		out = append(out, it)
	}

	sort.Slice(out, func(a, b int) bool {
		return out[a].CreateRevision < out[b].CreateRevision
	})

	return out
}

// Run keeps the index synchronized with etcd. Blocks until ctx is done.
// On Watch compaction it resyncs (fresh Get, new Watch).
func (i *QueueIndex) Run(ctx context.Context) {
	for ctx.Err() == nil {
		rev := i.resync(ctx)

		if rev == 0 {
			return
		}

		i.watchFrom(ctx, rev+1)
	}
}

// resync fetches the full prefix, replaces the index, returns the snapshot
// revision (suitable for Watch(from=rev+1)). Returns 0 if ctx is done.
func (i *QueueIndex) resync(ctx context.Context) int64 {
	resp := Throw2(i.cli.Get(ctx, queuePrefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend),
	))

	fresh := make(map[string]QueueItem, len(resp.Kvs))

	for _, kv := range resp.Kvs {
		var task Task
		Throw(json.Unmarshal(kv.Value, &task))

		fresh[string(kv.Key)] = QueueItem{
			Task:           task,
			CreateRevision: kv.CreateRevision,
			Key:            string(kv.Key),
		}
	}

	i.mu.Lock()
	i.byKey = fresh
	i.mu.Unlock()

	i.signal()

	return resp.Header.Revision
}

// watchFrom consumes events from the given revision. Returns on ctx done
// or on compaction (caller should resync).
func (i *QueueIndex) watchFrom(ctx context.Context, startRev int64) {
	ch := i.cli.Watch(ctx, queuePrefix,
		clientv3.WithPrefix(),
		clientv3.WithRev(startRev),
	)

	for resp := range ch {
		if err := resp.Err(); err != nil {
			return
		}

		for _, ev := range resp.Events {
			i.applyEvent(ev)
		}

		i.signal()
	}
}

func (i *QueueIndex) applyEvent(ev *clientv3.Event) {
	i.mu.Lock()
	defer i.mu.Unlock()

	key := string(ev.Kv.Key)

	switch ev.Type {
	case clientv3.EventTypePut:
		var task Task
		Throw(json.Unmarshal(ev.Kv.Value, &task))

		i.byKey[key] = QueueItem{
			Task:           task,
			CreateRevision: ev.Kv.CreateRevision,
			Key:            key,
		}
	case clientv3.EventTypeDelete:
		delete(i.byKey, key)
	}
}
