package main

import (
	"context"

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
