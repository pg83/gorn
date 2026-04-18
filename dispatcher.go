package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type Dispatcher struct {
	cli      *clientv3.Client
	leader   *Leader
	cfg      *Config
	keyFiles []*os.File

	mu       sync.Mutex
	inflight map[string]struct{}

	wake chan struct{}
}

func NewDispatcher(cli *clientv3.Client, leader *Leader, cfg *Config, keyFiles []*os.File) *Dispatcher {
	return &Dispatcher{
		cli:      cli,
		leader:   leader,
		cfg:      cfg,
		keyFiles: keyFiles,
		inflight: make(map[string]struct{}),
		wake:     make(chan struct{}, 1),
	}
}

func (d *Dispatcher) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		exc := Try(func() {
			d.watchQueue(ctx)
		})

		exc.Catch(func(e *Exception) {
			fmt.Fprintln(os.Stderr, "watchQueue failed:", e.Error())
		})
	}()

	for i, ep := range d.cfg.Endpoints {
		wg.Add(1)

		go func(ep Endpoint, keyFile *os.File) {
			defer wg.Done()

			d.endpointLoop(ctx, ep, keyFile)
		}(ep, d.keyFiles[i])
	}

	wg.Wait()
}

func (d *Dispatcher) watchQueue(ctx context.Context) {
	d.signal()

	ch := d.cli.Watch(ctx, queuePrefix, clientv3.WithPrefix())

	for range ch {
		d.signal()
	}
}

func (d *Dispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) endpointLoop(ctx context.Context, ep Endpoint, keyFile *os.File) {
	for ctx.Err() == nil {
		exc := Try(func() {
			d.oneIteration(ctx, ep, keyFile)
		})

		exc.Catch(func(e *Exception) {
			fmt.Fprintln(os.Stderr, ep.Host, ep.User, "iteration error:", e.Error())

			time.Sleep(3 * time.Second)
		})
	}
}

func (d *Dispatcher) oneIteration(ctx context.Context, ep Endpoint, keyFile *os.File) {
	task, ok := d.pickNextTask(ctx)

	if !ok {
		select {
		case <-ctx.Done():
		case <-d.wake:
		case <-time.After(5 * time.Second):
		}

		return
	}

	defer d.releaseInflight(task.GUID)

	outcome, detail := runTaskOnEndpoint(ctx, ep, task, d.cfg.S3, keyFile)

	fmt.Fprintf(os.Stderr, "task %s on %s@%s: %s (%s)\n", task.GUID, ep.User, ep.Host, outcome, detail)

	switch outcome {
	case OutcomeSuccess, OutcomeNonRetriable:
		d.finalize(ctx, task.GUID)
	case OutcomeRetriable:
		time.Sleep(3 * time.Second)
	}
}

func (d *Dispatcher) pickNextTask(ctx context.Context) (Task, bool) {
	items := queueList(ctx, d.cli)

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, it := range items {
		if _, busy := d.inflight[it.Task.GUID]; busy {
			continue
		}

		d.inflight[it.Task.GUID] = struct{}{}

		return it.Task, true
	}

	return Task{}, false
}

func (d *Dispatcher) releaseInflight(guid string) {
	d.mu.Lock()
	delete(d.inflight, guid)
	d.mu.Unlock()

	d.signal()
}

func (d *Dispatcher) finalize(ctx context.Context, guid string) {
	key := queueKey(guid)

	resp := Throw2(d.cli.Txn(ctx).
		If(d.leader.FenceCompare()).
		Then(clientv3.OpDelete(key)).
		Commit())

	if !resp.Succeeded {
		fmt.Fprintln(os.Stderr, "finalize skipped for", guid, "— no longer leader")
	}
}
