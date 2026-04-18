package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

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

	go func() {
		<-leader.Done()
		fmt.Fprintln(os.Stderr, "lost leadership — exiting immediately")
		os.Exit(0)
	}()

	disp := NewDispatcher(cli, leader, cfg, keyFiles)
	disp.Run(ctx)

	leader.Resign(context.Background())
}
