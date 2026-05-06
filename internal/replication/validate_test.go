package replication

import "testing"

func TestValidateConfigSuccess(t *testing.T) {
	cfg := Config{
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

	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidateConfigRejectsSameHost(t *testing.T) {
	cfg := Config{
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
			Host:         "10.0.0.10",
			Port:         5432,
			DataDir:      "/pg/standby",
			PostgresUser: "postgres",
			SSHUser:      "ubuntu",
			ServerID:     "b",
			DSN:          "postgres://postgres:secret@10.0.0.10:5432/postgres?sslmode=disable",
		},
		Network: Network{
			ReplicationCIDR: "10.0.0.11/32",
			ApplicationName: "orders-standby",
		},
	}

	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateConfigMasterMasterSuccess(t *testing.T) {
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

	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("expected valid master-master config, got error: %v", err)
	}
}
