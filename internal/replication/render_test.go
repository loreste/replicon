package replication

import (
	"strings"
	"testing"
)

func TestRenderTargetPrimary(t *testing.T) {
	cfg := testConfig()
	out, err := RenderTarget(cfg, "primary")
	if err != nil {
		t.Fatalf("render primary: %v", err)
	}
	if !strings.Contains(out, "pg_stat_replication") {
		t.Fatalf("expected verification query in output: %s", out)
	}
}

func TestRenderTargetStandby(t *testing.T) {
	cfg := testConfig()
	out, err := RenderTarget(cfg, "standby")
	if err != nil {
		t.Fatalf("render standby: %v", err)
	}
	if !strings.Contains(out, "pg_basebackup") {
		t.Fatalf("expected pg_basebackup in output: %s", out)
	}
}

func testConfig() Config {
	return Config{
		ClusterName:         "orders",
		Mode:                "master-slave",
		ReplicationUser:     "replicator",
		ReplicationSlot:     "orders_slot",
		Primary: NodeConfig{
			Name:         "primary",
			Host:         "10.0.0.10",
			Port:         5432,
			DataDir:      "/pg/primary",
			PostgresUser: "postgres",
			SSHUser:      "ubuntu",
			ServerID:     "a",
			DSN:          "postgres://postgres:secret@10.0.0.10:5432/postgres?sslmode=disable",
		},
		Standby: NodeConfig{
			Name:         "standby",
			Host:         "10.0.0.11",
			Port:         5432,
			DataDir:      "/pg/standby",
			PostgresUser: "postgres",
			SSHUser:      "ubuntu",
			ServerID:     "b",
			DSN:          "postgres://postgres:secret@10.0.0.11:5432/postgres?sslmode=disable",
		},
		Network: Network{
			ReplicationCIDR: "10.0.0.11/32",
			ApplicationName: "orders-standby",
		},
	}
}

func TestRenderTargetLogical(t *testing.T) {
	cfg := Config{
		ClusterName:         "orders",
		Mode:                "master-master",
		ReplicationUser:     "replicator",
		NodeA: NodeConfig{
			Name:         "node-a",
			Host:         "10.0.0.10",
			Port:         5432,
			DataDir:      "/pg/node-a",
			PostgresUser: "postgres",
			SSHUser:      "ubuntu",
			ServerID:     "a",
			DSN:          "postgres://postgres:secret@10.0.0.10:5432/appdb?sslmode=disable",
		},
		NodeB: NodeConfig{
			Name:         "node-b",
			Host:         "10.0.0.11",
			Port:         5432,
			DataDir:      "/pg/node-b",
			PostgresUser: "postgres",
			SSHUser:      "ubuntu",
			ServerID:     "b",
			DSN:          "postgres://postgres:secret@10.0.0.11:5432/appdb?sslmode=disable",
		},
		Logical: LogicalSetup{
			Database:        "appdb",
			PublicationA:    "pub_a",
			PublicationB:    "pub_b",
			SubscriptionA:   "sub_a",
			SubscriptionB:   "sub_b",
			ReplicationCIDR: "10.0.0.0/24",
		},
	}

	out, err := RenderTarget(cfg, "node-a")
	if err != nil {
		t.Fatalf("render logical: %v", err)
	}
	if !strings.Contains(out, "CREATE SUBSCRIPTION") {
		t.Fatalf("expected logical subscription output: %s", out)
	}
	if !strings.Contains(out, "origin = none") {
		t.Fatalf("expected subscription to avoid republishing replicated changes: %s", out)
	}
}
