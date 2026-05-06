package replication

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWatchdogDefaults(t *testing.T) {
	f := WatchdogDefaults(Failover{})
	if f.CheckIntervalSec != 5 {
		t.Fatalf("expected CheckIntervalSec=5, got %d", f.CheckIntervalSec)
	}
	if f.HealthTimeoutSec != 3 {
		t.Fatalf("expected HealthTimeoutSec=3, got %d", f.HealthTimeoutSec)
	}
	if f.MaxFailures != 3 {
		t.Fatalf("expected MaxFailures=3, got %d", f.MaxFailures)
	}
	if f.FenceTimeoutSec != 10 {
		t.Fatalf("expected FenceTimeoutSec=10, got %d", f.FenceTimeoutSec)
	}
}

func TestWatchdogDefaultsPreservesExplicit(t *testing.T) {
	f := WatchdogDefaults(Failover{
		CheckIntervalSec: 10,
		HealthTimeoutSec: 7,
		MaxFailures:      5,
		FenceTimeoutSec:  30,
	})
	if f.CheckIntervalSec != 10 {
		t.Fatalf("expected CheckIntervalSec=10, got %d", f.CheckIntervalSec)
	}
	if f.MaxFailures != 5 {
		t.Fatalf("expected MaxFailures=5, got %d", f.MaxFailures)
	}
}

func TestWatchdogRejectsMasterMaster(t *testing.T) {
	cfg := Config{
		ClusterName:     "test",
		Mode:            "master-master",
		ReplicationUser: "replicator",
		Failover:        Failover{Enabled: true, MaxFailures: 3},
	}
	err := RunWatchdog(context.Background(), cfg, nil, WatchdogCallbacks{})
	if err == nil {
		t.Fatal("expected error for master-master mode")
	}
	if err.Error() != "automatic failover is only supported for master-slave mode" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWatchdogRejectsMissingDSN(t *testing.T) {
	cfg := Config{
		ClusterName:     "test",
		Mode:            "master-slave",
		ReplicationUser: "replicator",
		ReplicationSlot: "slot",
		Primary: NodeConfig{
			Name: "p", Host: "10.0.0.1", Port: 5432,
			DataDir: "/d", PostgresUser: "pg", SSHUser: "u", ServerID: "a",
			// No DSN or DSNEnv
		},
		Failover: Failover{Enabled: true, MaxFailures: 3},
	}
	err := RunWatchdog(context.Background(), cfg, nil, WatchdogCallbacks{})
	if err == nil {
		t.Fatal("expected error for missing DSN")
	}
}

func TestWatchdogCancelledBeforeFailure(t *testing.T) {
	cfg := Config{
		ClusterName:     "test",
		Mode:            "master-slave",
		ReplicationUser: "replicator",
		ReplicationSlot: "slot",
		Primary: NodeConfig{
			Name: "p", Host: "10.0.0.1", Port: 5432,
			DataDir: "/d", PostgresUser: "pg", SSHUser: "u", ServerID: "a",
			DSN: "postgres://postgres:fake@127.0.0.1:59999/postgres",
		},
		Standby: NodeConfig{
			Name: "s", Host: "10.0.0.2", Port: 5432,
			DataDir: "/d", PostgresUser: "pg", SSHUser: "u", ServerID: "b",
			DSN: "postgres://postgres:fake@127.0.0.2:59999/postgres",
		},
		Network: Network{ReplicationCIDR: "10.0.0.2/32", ApplicationName: "test"},
		Failover: Failover{
			Enabled:          true,
			CheckIntervalSec: 1,
			HealthTimeoutSec: 1,
			MaxFailures:      100, // high threshold so we cancel before reaching it
			FenceTimeoutSec:  5,
		},
	}

	var mu sync.Mutex
	var events []WatchdogEvent

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := RunWatchdog(ctx, cfg, nil, WatchdogCallbacks{
		OnEvent: func(e WatchdogEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
	})

	if err != context.DeadlineExceeded {
		t.Fatalf("expected context deadline exceeded, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Should have the startup event plus some failure events.
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	if events[0].Type != "healthy" {
		t.Fatalf("expected first event type 'healthy' (startup), got %q", events[0].Type)
	}

	// At least one failure should have been recorded.
	hasFailure := false
	for _, e := range events {
		if e.Type == "failure" {
			hasFailure = true
			break
		}
	}
	if !hasFailure {
		t.Fatal("expected at least one failure event")
	}
}

func TestCheckPrimaryHealthUnreachable(t *testing.T) {
	// Unreachable host — should return false quickly.
	ok := checkPrimaryHealth(context.Background(), "postgres://postgres:fake@127.0.0.1:59999/postgres", 2*time.Second)
	if ok {
		t.Fatal("expected unreachable primary to return false")
	}
}

func TestValidateFailoverConfig(t *testing.T) {
	base := Config{
		ClusterName:     "test",
		Mode:            "master-slave",
		ReplicationUser: "replicator",
		ReplicationSlot: "slot",
		Primary: NodeConfig{
			Name: "p", Host: "10.0.0.1", Port: 5432,
			DataDir: "/d", PostgresUser: "pg", SSHUser: "u", ServerID: "a",
			DSN: "postgres://postgres:x@10.0.0.1:5432/postgres",
		},
		Standby: NodeConfig{
			Name: "s", Host: "10.0.0.2", Port: 5432,
			DataDir: "/d", PostgresUser: "pg", SSHUser: "u", ServerID: "b",
			DSN: "postgres://postgres:x@10.0.0.2:5432/postgres",
		},
		Network: Network{ReplicationCIDR: "10.0.0.2/32", ApplicationName: "test"},
	}

	t.Run("valid failover config", func(t *testing.T) {
		cfg := base
		cfg.Failover = Failover{Enabled: true, MaxFailures: 3}
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})

	t.Run("failover disabled passes without checks", func(t *testing.T) {
		cfg := base
		cfg.Failover = Failover{Enabled: false, MaxFailures: -1}
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("expected valid when disabled, got: %v", err)
		}
	})

	t.Run("failover rejects master-master", func(t *testing.T) {
		cfg := base
		cfg.Mode = "master-master"
		cfg.Failover = Failover{Enabled: true, MaxFailures: 3}
		// Need to fill in master-master fields to pass mode validation
		cfg.NodeA = cfg.Primary
		cfg.NodeB = cfg.Standby
		cfg.Logical = LogicalSetup{
			Database: "db", PublicationA: "pa", PublicationB: "pb",
			SubscriptionA: "sa", SubscriptionB: "sb", ReplicationCIDR: "10.0.0.0/24",
		}
		err := ValidateConfig(cfg)
		if err == nil {
			t.Fatal("expected error")
		}
		if err.Error() != "failover.enabled is only supported for master-slave mode" {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("max_failures must be at least 1", func(t *testing.T) {
		cfg := base
		cfg.Failover = Failover{Enabled: true, MaxFailures: 0}
		err := ValidateConfig(cfg)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}
