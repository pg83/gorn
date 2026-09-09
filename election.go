package main

import (
	"context"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

type Leader struct {
	session  *concurrency.Session
	election *concurrency.Election
	id       string
}

func campaign(ctx context.Context, cli *clientv3.Client, id string) *Leader {
	sess := Throw2(concurrency.NewSession(cli, concurrency.WithTTL(15)))
	elect := concurrency.NewElection(sess, leaderElectionPrefix)

	Throw(elect.Campaign(ctx, id))

	return &Leader{
		session:  sess,
		election: elect,
		id:       id,
	}
}

func (l *Leader) Done() <-chan struct{} {
	return l.session.Done()
}

func (l *Leader) FenceCompare() clientv3.Cmp {
	return clientv3.Compare(clientv3.CreateRevision(l.election.Key()), "=", l.election.Rev())
}

func (l *Leader) ID() string {
	return l.id
}

func (l *Leader) Resign(ctx context.Context) {
	_ = l.election.Resign(ctx)
	_ = l.session.Close()
}

// leaderHost returns the hostname of the current leader, taken from the
// election key with the oldest CreateRevision — the same key concurrency.
// Election.Leader() resolves, read directly so callers need no session of
// their own. The value is the campaign id, "<hostname>/<pid>"; only the
// host part is of interest, since every host serves on the same port.
func leaderHost(ctx context.Context, cli *clientv3.Client) (string, bool) {
	resp := Throw2(cli.Get(ctx, leaderElectionPrefix,
		clientv3.WithFirstCreate()...))

	if len(resp.Kvs) == 0 {
		return "", false
	}

	id := string(resp.Kvs[0].Value)
	host, _, _ := strings.Cut(id, "/")

	if host == "" {
		return "", false
	}

	return host, true
}
