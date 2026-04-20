package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// promMain serves /metrics in Prometheus text exposition format. Each scrape
// issues one etcd Range over the queue prefix; the set is small (queue depth
// grows with backlog, not with fleet) so a per-request query beats a bg
// poller. Endpoint count and SSH key count come from the static config.
func promMain(args []string) {
	fs := flag.NewFlagSet("prom", flag.ExitOnError)
	configPath := fs.String("config", "", "path to JSON config (required)")
	listen := fs.String("listen", "", "override config.prom.listen")
	Throw(fs.Parse(args))

	if *configPath == "" {
		ThrowFmt("prom: --config is required")
	}

	cfg := LoadConfig(*configPath)

	addr := *listen

	if addr == "" {
		addr = cfg.Prom.Listen
	}

	if addr == "" {
		ThrowFmt("prom: listen address required via --listen or config.prom.listen")
	}

	if len(cfg.Etcd.Endpoints) == 0 {
		ThrowFmt("prom: etcd.endpoints is required in config")
	}

	cli := newEtcdClient(cfg.Etcd)
	defer cli.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, cli, cfg)
	})

	fmt.Fprintf(os.Stderr, "prom: listening on %s\n", addr)
	Throw(http.ListenAndServe(addr, mux))
}

func writeMetrics(w io.Writer, cli *clientv3.Client, cfg *Config) {
	fmt.Fprintln(w, "# HELP gorn_up 1 while the prom scraper is serving.")
	fmt.Fprintln(w, "# TYPE gorn_up gauge")
	fmt.Fprintln(w, "gorn_up 1")

	fmt.Fprintln(w, "# HELP gorn_endpoint_count Number of configured worker endpoints.")
	fmt.Fprintln(w, "# TYPE gorn_endpoint_count gauge")
	fmt.Fprintf(w, "gorn_endpoint_count %d\n", len(cfg.Endpoints))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	items, exc := listQueueSafe(ctx, cli)

	if exc != nil {
		fmt.Fprintf(w, "# gorn_queue_depth unavailable: %s\n", exc.Error())

		return
	}

	fmt.Fprintln(w, "# HELP gorn_queue_depth Tasks currently queued in etcd.")
	fmt.Fprintln(w, "# TYPE gorn_queue_depth gauge")
	fmt.Fprintf(w, "gorn_queue_depth %d\n", len(items))

	fmt.Fprintln(w, "# HELP gorn_queue_oldest_age_seconds Age of the oldest queued task, in seconds; absent when the queue is empty.")
	fmt.Fprintln(w, "# TYPE gorn_queue_oldest_age_seconds gauge")

	if age, ok := oldestQueuedAge(items, time.Now()); ok {
		fmt.Fprintf(w, "gorn_queue_oldest_age_seconds %f\n", age.Seconds())
	}
}

func listQueueSafe(ctx context.Context, cli *clientv3.Client) (items []QueueItem, exc *Exception) {
	exc = Try(func() {
		items = queueList(ctx, cli)
	})

	return
}

func oldestQueuedAge(items []QueueItem, now time.Time) (time.Duration, bool) {
	var oldest time.Duration
	found := false

	for _, it := range items {
		if it.Task.EnqueuedAt == "" {
			continue
		}

		t, err := time.Parse(time.RFC3339Nano, it.Task.EnqueuedAt)

		if err != nil {
			continue
		}

		age := now.Sub(t)

		if !found || age > oldest {
			oldest = age
			found = true
		}
	}

	return oldest, found
}
