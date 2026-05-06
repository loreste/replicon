package replication

import (
	"context"
	"testing"
	"time"
)

func TestElectionConfigResolveDSN(t *testing.T) {
	t.Run("inline dsn", func(t *testing.T) {
		e := ElectionConfig{DSN: "postgres://localhost/test"}
		dsn, err := e.ResolveDSN()
		if err != nil {
			t.Fatal(err)
		}
		if dsn != "postgres://localhost/test" {
			t.Fatalf("expected inline dsn, got %q", dsn)
		}
	})

	t.Run("env dsn", func(t *testing.T) {
		t.Setenv("REPLICON_TEST_ELECTION_DSN", "postgres://envhost/test")
		e := ElectionConfig{DSNEnv: "REPLICON_TEST_ELECTION_DSN"}
		dsn, err := e.ResolveDSN()
		if err != nil {
			t.Fatal(err)
		}
		if dsn != "postgres://envhost/test" {
			t.Fatalf("expected env dsn, got %q", dsn)
		}
	})

	t.Run("missing", func(t *testing.T) {
		e := ElectionConfig{}
		_, err := e.ResolveDSN()
		if err == nil {
			t.Fatal("expected error for missing dsn")
		}
	})
}

func TestLeaderElectionIsLeaderDefault(t *testing.T) {
	le := NewLeaderElection(ElectionConfig{NodeID: "test"}, "cluster")
	if le.IsLeader() {
		t.Fatal("new election should not be leader")
	}
}

func TestLeaderElectionSetLeader(t *testing.T) {
	le := NewLeaderElection(ElectionConfig{NodeID: "test"}, "cluster")
	le.setLeader(true)
	if !le.IsLeader() {
		t.Fatal("expected leader after setLeader(true)")
	}
	le.setLeader(false)
	if le.IsLeader() {
		t.Fatal("expected not leader after setLeader(false)")
	}
}

func TestLeaderElectionRunRejectsMissingDSN(t *testing.T) {
	le := NewLeaderElection(ElectionConfig{NodeID: "test"}, "cluster")
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err := le.Run(ctx, func(e WatchdogEvent) {})
	if err == nil {
		t.Fatal("expected error for missing DSN")
	}
}

func TestLeaderElectionConcurrentAccess(t *testing.T) {
	le := NewLeaderElection(ElectionConfig{NodeID: "test"}, "cluster")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			le.setLeader(i%2 == 0)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = le.IsLeader()
	}
	<-done
}
