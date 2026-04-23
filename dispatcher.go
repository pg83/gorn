package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"golang.org/x/sync/semaphore"
)

type Dispatcher struct {
	cli      *clientv3.Client
	leader   *Leader
	cfg      *Config
	keyFiles []*os.File
	index    *QueueIndex

	hosts     map[string]*hostState
	hostNames []string // sorted; deterministic iteration

	mu       sync.Mutex
	inflight map[string]struct{}

	wake chan struct{}
}

// hostState owns a host's capacity and endpoint pool. Sem meters concurrent
// slot usage; freeEps hands out the actual gorn_N endpoint (user+ssh_key) a
// dispatched task will SSH through. cpusPerSlot * task.Slots gives MOLOT_CPUS.
type hostState struct {
	capacity    int64
	sem         *semaphore.Weighted
	freeEps     chan *endpointRef
	cpusPerSlot int
}

type endpointRef struct {
	ep      Endpoint
	keyFile *os.File
}

func NewDispatcher(cli *clientv3.Client, leader *Leader, cfg *Config, keyFiles []*os.File) *Dispatcher {
	hosts := map[string]*hostState{}

	for i, ep := range cfg.Endpoints {
		h := hosts[ep.Host]

		if h == nil {
			hc, ok := cfg.Hosts[ep.Host]

			if !ok || hc.CpusPerSlot <= 0 {
				ThrowFmt("dispatcher: host %q missing cpus_per_slot in config.hosts (required for endpoints using it)", ep.Host)
			}

			h = &hostState{cpusPerSlot: hc.CpusPerSlot}
			hosts[ep.Host] = h
		}

		h.capacity++
		h.freeEps = extendEPs(h.freeEps, &endpointRef{ep: ep, keyFile: keyFiles[i]})
	}

	names := make([]string, 0, len(hosts))

	for name, h := range hosts {
		h.sem = semaphore.NewWeighted(h.capacity)
		names = append(names, name)
	}

	sort.Strings(names)

	return &Dispatcher{
		cli:       cli,
		leader:    leader,
		cfg:       cfg,
		keyFiles:  keyFiles,
		index:     NewQueueIndex(cli),
		hosts:     hosts,
		hostNames: names,
		inflight:  make(map[string]struct{}),
		wake:      make(chan struct{}, 1),
	}
}

// extendEPs appends ref to the buffered channel, growing the buffer by one.
// Channels can't be resized; build a new channel one slot bigger and copy.
func extendEPs(old chan *endpointRef, ref *endpointRef) chan *endpointRef {
	size := 1

	if old != nil {
		size = cap(old) + 1
	}

	out := make(chan *endpointRef, size)

	if old != nil {
		close(old)

		for r := range old {
			out <- r
		}
	}

	out <- ref

	return out
}

func (d *Dispatcher) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		exc := Try(func() {
			d.index.Run(ctx)
		})

		exc.Catch(func(e *Exception) {
			fmt.Fprintln(os.Stderr, "queue index failed:", e.Error())
		})
	}()

	wg.Add(1)

	go func() {
		defer wg.Done()

		d.forwardWakes(ctx)
	}()

	wg.Add(1)

	go func() {
		defer wg.Done()

		d.schedulerLoop(ctx)
	}()

	wg.Wait()
}

func (d *Dispatcher) forwardWakes(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.index.Wake():
			d.signal()
		}
	}
}

func (d *Dispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) schedulerLoop(ctx context.Context) {
	for ctx.Err() == nil {
		exc := Try(func() {
			d.pickAll(ctx)
		})

		exc.Catch(func(e *Exception) {
			fmt.Fprintln(os.Stderr, "scheduler iteration error:", e.Error())

			time.Sleep(3 * time.Second)
		})

		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-time.After(5 * time.Second):
		}
	}
}

// pickAll scans the queue in priority order, tries to dispatch every eligible
// task to a host with capacity. Skips tasks whose slot count exceeds every
// host's capacity (unschedulable — already rejected at enqueue, but defense
// in depth). Tasks that fit but can't acquire right now wait for the next
// wake (freed sem or new enqueue).
func (d *Dispatcher) pickAll(ctx context.Context) {
	for _, it := range d.index.Snapshot() {
		task := it.Task
		slots := task.Slots

		if slots <= 0 {
			slots = 1
		}

		d.mu.Lock()

		if _, busy := d.inflight[task.GUID]; busy {
			d.mu.Unlock()

			continue
		}

		host, ref := d.tryAcquire(int64(slots))

		if host == nil {
			d.mu.Unlock()

			continue
		}

		d.inflight[task.GUID] = struct{}{}
		d.mu.Unlock()

		go d.runTask(ctx, task, slots, ref, host)
	}
}

// tryAcquire walks hosts in sorted order, first-fit: picks the first host
// that has a) enough total capacity for the task, and b) sem.TryAcquire
// succeeds right now. Returns the acquired endpoint ref, or (nil, nil).
// Caller holds d.mu.
func (d *Dispatcher) tryAcquire(slots int64) (*hostState, *endpointRef) {
	for _, name := range d.hostNames {
		h := d.hosts[name]

		if slots > h.capacity {
			continue
		}

		if !h.sem.TryAcquire(slots) {
			continue
		}

		// We just acquired slots; a free endpoint must be available.
		var ref *endpointRef

		select {
		case ref = <-h.freeEps:
		default:
			// Shouldn't happen — sem gates count of in-flight tasks per
			// host to len(freeEps). Release and move on.
			h.sem.Release(slots)

			continue
		}

		return h, ref
	}

	return nil, nil
}

func (d *Dispatcher) runTask(ctx context.Context, task Task, slots int, ref *endpointRef, host *hostState) {
	defer func() {
		host.sem.Release(int64(slots))
		host.freeEps <- ref

		d.mu.Lock()
		delete(d.inflight, task.GUID)
		d.mu.Unlock()

		d.signal()
	}()

	// Try boundary so a panic from inside (SSH dispatch, etcd txn,
	// anything Throw-family) doesn't kill the whole gorn serve process.
	// The inflight slot gets released via the defer above regardless.
	exc := Try(func() {
		if task.Env == nil {
			task.Env = map[string]string{}
		}

		cpus := int(float64(slots*host.cpusPerSlot)*d.cfg.CpuOvercommit + 0.5)

		task.Env["MOLOT_SLOTS"] = strconv.Itoa(slots)
		task.Env["MOLOT_CPUS"] = strconv.Itoa(cpus)

		outcome, detail := runTaskOnEndpoint(ctx, ref.ep, task, d.cfg.S3, ref.keyFile)

		fmt.Fprintf(os.Stderr, "task %s on %s@%s (slots=%d cpus=%d): %s (%s)\n", task.GUID, ref.ep.User, ref.ep.Host, slots, cpus, outcome, detail)

		switch outcome {
		case OutcomeSuccess, OutcomeNonRetriable:
			d.finalize(ctx, task.GUID)
		case OutcomeRetriable:
			time.Sleep(3 * time.Second)
		}
	})

	exc.Catch(func(e *Exception) {
		fmt.Fprintf(os.Stderr, "runTask %s: caught panic: %s\n", task.GUID, e.Error())
	})
}

// finalize deletes the task's queue key via a leader-fenced txn. Under
// heavy etcd load the txn times out; we retry with exponential backoff
// a handful of times before giving up. If we give up, the task stays
// in the queue and gets re-dispatched on the next pickAll pass — gorn
// wrap's S3 idempotency makes that cheap (already-done), but the
// churn is ugly so we prefer to actually delete when possible.
//
// `resp.Succeeded == false` (fence compare failed) means we lost
// leadership between dispatch and finalize. Nothing to retry —
// somebody else owns the queue now.
func (d *Dispatcher) finalize(ctx context.Context, guid string) {
	const maxAttempts = 5

	key := queueKey(guid)
	delay := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := d.cli.Txn(ctx).
			If(d.leader.FenceCompare()).
			Then(clientv3.OpDelete(key)).
			Commit()

		if err == nil {
			if !resp.Succeeded {
				fmt.Fprintln(os.Stderr, "finalize skipped for", guid, "— no longer leader")
			}

			return
		}

		if attempt == maxAttempts {
			fmt.Fprintf(os.Stderr, "finalize %s: gave up after %d attempts (%v); task stays queued, will be re-dispatched\n", guid, maxAttempts, err)

			return
		}

		sleep := delay + time.Duration(rand.Int64N(int64(delay)))
		fmt.Fprintf(os.Stderr, "finalize %s: attempt %d/%d failed (%v), retrying in %v\n", guid, attempt, maxAttempts, err, sleep)
		time.Sleep(sleep)

		delay *= 2
	}
}
