package main

import (
	"context"
	"encoding/json"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Schema-versioned prefix: bump the suffix whenever the Task JSON layout
// changes incompatibly (field renames, removals, semantic shifts). Old
// entries under previous prefixes are left alone for etcd's compactor to
// eventually reap — no online migration, no half-deserialized rows.
//
//   queue_v1 — Cmd []string (up to 2026-04-20)
//   queue_v2 — Script string (current)
const (
	queuePrefix          = "/gorn/queue_v2/"
	leaderElectionPrefix = "/gorn/election/"
)

func queueKey(guid string) string {
	return queuePrefix + guid
}

func newEtcdClient(cfg EtcdConfig) *clientv3.Client {
	cli := Throw2(clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: 10 * time.Second,
	}))

	return cli
}

type QueueItem struct {
	Task           Task
	CreateRevision int64
	Key            string
}

func queueList(ctx context.Context, cli *clientv3.Client) []QueueItem {
	resp := Throw2(cli.Get(ctx, queuePrefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend),
	))

	items := make([]QueueItem, 0, len(resp.Kvs))

	for _, kv := range resp.Kvs {
		var task Task
		Throw(json.Unmarshal(kv.Value, &task))

		items = append(items, QueueItem{
			Task:           task,
			CreateRevision: kv.CreateRevision,
			Key:            string(kv.Key),
		})
	}

	return items
}
