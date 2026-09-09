package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// serveInflight exposes what this process is running right now. It starts
// only after the campaign is won, so a reachable handle is by construction
// the leader's. Returns a shutdown func; a listen failure is logged rather
// than fatal — losing the view must not take the dispatcher down with it.
func serveInflight(addr string, disp *Dispatcher) func() {
	if addr == "" {
		return func() {}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/v1/inflight", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpError(w, http.StatusMethodNotAllowed, "method not allowed")

			return
		}

		httpJSON(w, http.StatusOK, InflightResp{Inflight: disp.Inflight()})
	})

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		fmt.Fprintln(os.Stderr, "serve: inflight handle on", addr)

		err := srv.ListenAndServe()

		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "serve: inflight handle stopped:", err)
		}
	}()

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = srv.Shutdown(ctx)
	}
}

func serveMain(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "path to JSON config (required)")
	Throw(fs.Parse(args))

	if *configPath == "" {
		ThrowFmt("serve: --config is required")
	}

	cfg := LoadConfig(*configPath)

	if len(cfg.Endpoints) == 0 {
		ThrowFmt("serve: at least one endpoint is required in config")
	}

	if len(cfg.Etcd.Endpoints) == 0 {
		ThrowFmt("serve: etcd.endpoints is required in config")
	}

	for i, ep := range cfg.Endpoints {
		if ep.SSHKey == "" && cfg.SSHKeyPath == "" {
			ThrowFmt("serve: endpoint %d (%s@%s) has no ssh_key and global ssh_key_path is unset", i, ep.User, ep.Host)
		}

		hc, ok := cfg.Hosts[ep.Host]

		if !ok || hc.CpusPerSlot <= 0 {
			ThrowFmt("serve: endpoint %d (%s@%s) references host %q which is missing or has cpus_per_slot<=0 in config.hosts", i, ep.User, ep.Host, ep.Host)
		}
	}

	if cfg.SSHKeyPath != "" {
		Throw2(os.Stat(cfg.SSHKeyPath))
	}

	keyFiles, cleanupKeys := materializeSSHKeys(cfg.Endpoints, cfg.SSHKeyPath)
	defer cleanupKeys()

	cli := newEtcdClient(cfg.Etcd)
	defer cli.Close()

	host := Throw2(os.Hostname())
	id := fmt.Sprintf("%s/%d", host, os.Getpid())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		fmt.Fprintln(os.Stderr, "received signal:", sig)
		cancel()
	}()

	fmt.Fprintln(os.Stderr, "campaigning for leadership as", id)

	leader := campaign(ctx, cli, id)

	fmt.Fprintln(os.Stderr, "became leader")

	// When the etcd session dies (lease expired, explicit close on our
	// exit, transport blip), cancel the parent ctx so the dispatcher
	// winds down. Don't os.Exit(0) here — that used to race ahead of
	// any panic in Run and swallow the stacktrace, making "lost
	// leadership ??? exiting immediately" loops impossible to diagnose.
	go func() {
		<-leader.Done()
		fmt.Fprintln(os.Stderr, "session done — cancelling dispatcher")
		cancel()
	}()

	disp := NewDispatcher(cli, leader, cfg, keyFiles)

	stopInflight := serveInflight(cfg.Serve.Listen, disp)
	defer stopInflight()

	disp.Run(ctx)

	leader.Resign(context.Background())
}
