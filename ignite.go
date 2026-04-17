package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type stringsFlag []string

func (s *stringsFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringsFlag) Set(v string) error {
	*s = append(*s, v)

	return nil
}

func igniteMain(args []string) {
	fs := flag.NewFlagSet("ignite", flag.ExitOnError)

	configPath := fs.String("config", "", "path to JSON config (required)")
	guid := fs.String("guid", "", "task GUID (required)")

	var envs stringsFlag
	fs.Var(&envs, "env", "KEY=VALUE (repeatable)")

	Throw(fs.Parse(args))

	if *configPath == "" {
		ThrowFmt("ignite: --config is required")
	}

	if *guid == "" {
		ThrowFmt("ignite: --guid is required")
	}

	cmdArgs := fs.Args()

	if len(cmdArgs) == 0 {
		ThrowFmt("ignite: command is required after flags (use -- to separate)")
	}

	cfg := LoadConfig(*configPath)

	task := Task{
		GUID: *guid,
		Cmd:  cmdArgs,
		Env:  parseEnvs(envs),
	}

	enqueueTask(cfg, task)

	fmt.Println(task.GUID)
}

func parseEnvs(envs []string) map[string]string {
	if len(envs) == 0 {
		return nil
	}

	out := make(map[string]string, len(envs))

	for _, e := range envs {
		idx := strings.IndexByte(e, '=')

		if idx <= 0 {
			ThrowFmt("ignite: --env %q must be KEY=VALUE with non-empty KEY", e)
		}

		out[e[:idx]] = e[idx+1:]
	}

	return out
}

func enqueueTask(cfg *Config, task Task) {
	cli := newEtcdClient(cfg.Etcd)
	defer cli.Close()

	payload := Throw2(json.Marshal(task))

	key := queueKey(task.GUID)

	ctx := context.Background()

	resp := Throw2(cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(payload))).
		Commit())

	if !resp.Succeeded {
		ThrowFmt("ignite: task with guid %q already exists in queue", task.GUID)
	}
}
