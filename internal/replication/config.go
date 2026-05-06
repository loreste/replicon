package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

type Config struct {
	ClusterName     string       `json:"cluster_name"`
	Mode            string       `json:"mode"`
	ReplicationUser string       `json:"replication_user"`
	ReplicationSlot string       `json:"replication_slot"`
	Primary         NodeConfig   `json:"primary"`
	Standby         NodeConfig   `json:"standby"`
	Standbys        []NodeConfig `json:"standbys,omitempty"`
	NodeA           NodeConfig   `json:"node_a"`
	NodeB           NodeConfig   `json:"node_b"`
	Network         Network      `json:"network"`
	Logical         LogicalSetup `json:"logical"`
	Failover        Failover     `json:"failover,omitempty"`
}

// ResolveStandbys returns the effective list of standbys. If the Standbys
// slice is populated (cluster mode), it is returned. Otherwise the single
// Standby field is returned as a one-element slice. Returns nil if neither
// is configured.
func (c Config) ResolveStandbys() []NodeConfig {
	if len(c.Standbys) > 0 {
		return c.Standbys
	}
	if c.Standby.Name != "" {
		return []NodeConfig{c.Standby}
	}
	return nil
}

// IsCluster returns true when the config defines multiple standbys.
func (c Config) IsCluster() bool {
	return len(c.Standbys) > 1
}

// Failover configures the automatic failover watchdog.
type Failover struct {
	Enabled            bool          `json:"enabled"`
	CheckIntervalSec   int           `json:"check_interval_sec"`
	HealthTimeoutSec   int           `json:"health_timeout_sec"`
	MaxFailures        int           `json:"max_failures"`
	FenceTimeoutSec    int           `json:"fence_timeout_sec"`
	FenceCommand       string        `json:"fence_command,omitempty"`
	PostPromoteCommand string        `json:"post_promote_command,omitempty"`
	Witness            WitnessConfig `json:"witness,omitempty"`
}

// WitnessConfig defines an independent observer that breaks ties when the
// watchdog cannot fence the primary via SSH. If both the watchdog and the
// witness agree the primary is unreachable, promotion proceeds without
// fencing — the two-observer agreement replaces the fence as the
// split-brain prevention mechanism.
type WitnessConfig struct {
	Enabled bool   `json:"enabled"`
	DSN     string `json:"dsn,omitempty"`
	DSNEnv  string `json:"dsn_env,omitempty"`
}

func (w WitnessConfig) ResolveDSN() (string, error) {
	if value := strings.TrimSpace(w.DSN); value != "" {
		return value, nil
	}
	if envName := strings.TrimSpace(w.DSNEnv); envName != "" {
		value := strings.TrimSpace(os.Getenv(envName))
		if value == "" {
			return "", fmt.Errorf("environment variable %q is not set", envName)
		}
		return value, nil
	}
	return "", errors.New("witness dsn or dsn_env is required")
}

// ConflictResolution configures how logical replication conflicts are handled.
type ConflictResolution struct {
	Strategy string `json:"strategy"` // "last_write_wins", "skip", "log"
}

// DDLReplication configures automatic DDL tracking and replay for
// master-master logical replication.
type DDLReplication struct {
	Enabled bool `json:"enabled"`
}

type NodeConfig struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	DataDir      string `json:"data_dir"`
	PostgresUser string `json:"postgres_user"`
	SSHUser      string `json:"ssh_user"`
	ServerID     string `json:"server_id"`
	DSN          string `json:"dsn,omitempty"`
	DSNEnv       string `json:"dsn_env,omitempty"`
}

type Network struct {
	ReplicationCIDR string `json:"replication_cidr"`
	ApplicationName string `json:"application_name"`
}

type LogicalSetup struct {
	Database           string             `json:"database"`
	PublicationA       string             `json:"publication_a"`
	PublicationB       string             `json:"publication_b"`
	SubscriptionA      string             `json:"subscription_a"`
	SubscriptionB      string             `json:"subscription_b"`
	ReplicationCIDR    string             `json:"replication_cidr"`
	Nodes              []NodeConfig       `json:"nodes,omitempty"`
	ConflictResolution ConflictResolution `json:"conflict_resolution,omitempty"`
	DDLReplication     DDLReplication     `json:"ddl_replication,omitempty"`
}

// ResolveLogicalNodes returns the effective list of logical replication nodes.
// If Nodes is populated (multi-primary), it is returned. Otherwise NodeA and
// NodeB from the parent Config are returned as a two-element slice.
func (c Config) ResolveLogicalNodes() []NodeConfig {
	if len(c.Logical.Nodes) > 0 {
		return c.Logical.Nodes
	}
	if c.NodeA.Name != "" && c.NodeB.Name != "" {
		return []NodeConfig{c.NodeA, c.NodeB}
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("config path is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	return cfg, nil
}

func SampleConfig(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "master-slave":
		return `{
  "cluster_name": "orders-prod",
  "mode": "master-slave",
  "replication_user": "replicator",
  "replication_slot": "orders_prod_standby",
  "primary": {
    "name": "primary-a",
    "host": "10.0.0.10",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-a",
    "dsn_env": "REPLICON_PRIMARY_DSN"
  },
  "standby": {
    "name": "standby-b",
    "host": "10.0.0.11",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-b",
    "dsn_env": "REPLICON_STANDBY_DSN"
  },
  "network": {
    "replication_cidr": "10.0.0.11/32",
    "application_name": "orders-prod-standby"
  },
  "failover": {
    "enabled": false,
    "check_interval_sec": 5,
    "health_timeout_sec": 3,
    "max_failures": 3,
    "fence_timeout_sec": 10,
    "fence_command": "sudo systemctl stop postgresql",
    "post_promote_command": ""
  }
}
`, nil
	case "master-master":
		return `{
  "cluster_name": "orders-prod",
  "mode": "master-master",
  "replication_user": "replicator",
  "node_a": {
    "name": "writer-a",
    "host": "10.0.0.10",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-a",
    "dsn_env": "REPLICON_NODE_A_DSN"
  },
  "node_b": {
    "name": "writer-b",
    "host": "10.0.0.11",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-b",
    "dsn_env": "REPLICON_NODE_B_DSN"
  },
  "logical": {
    "database": "appdb",
    "publication_a": "orders_pub_a",
    "publication_b": "orders_pub_b",
    "subscription_a": "orders_sub_a",
    "subscription_b": "orders_sub_b",
    "replication_cidr": "10.0.0.0/24"
  }
}
`, nil
	default:
		return "", fmt.Errorf("unsupported mode %q", mode)
	}
}

func (n NodeConfig) ResolveDSN() (string, error) {
	if value := strings.TrimSpace(n.DSN); value != "" {
		return value, nil
	}
	if envName := strings.TrimSpace(n.DSNEnv); envName != "" {
		value := strings.TrimSpace(os.Getenv(envName))
		if value == "" {
			return "", fmt.Errorf("environment variable %q is not set", envName)
		}
		return value, nil
	}
	return "", errors.New("dsn or dsn_env is required")
}

func (c Config) toJSON() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

func isValidHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	// Reject hostnames that contain characters not allowed in DNS names.
	for _, r := range host {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.') {
			return false
		}
	}
	return len(host) <= 253
}
