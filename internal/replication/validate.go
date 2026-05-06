package replication

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/jackc/pgx/v5"
)

func ValidateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.ClusterName) == "" {
		return fmt.Errorf("cluster_name is required")
	}
	if strings.TrimSpace(cfg.Mode) == "" {
		return fmt.Errorf("mode is required")
	}
	if strings.TrimSpace(cfg.ReplicationUser) == "" {
		return fmt.Errorf("replication_user is required")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "master-slave":
		if strings.TrimSpace(cfg.ReplicationSlot) == "" {
			return fmt.Errorf("replication_slot is required for master-slave")
		}
		if strings.TrimSpace(cfg.Network.ReplicationCIDR) == "" {
			return fmt.Errorf("network.replication_cidr is required for master-slave")
		}
		if _, err := netip.ParsePrefix(cfg.Network.ReplicationCIDR); err != nil {
			return fmt.Errorf("network.replication_cidr is invalid: %w", err)
		}
		if strings.TrimSpace(cfg.Network.ApplicationName) == "" {
			return fmt.Errorf("network.application_name is required for master-slave")
		}
		if err := validateNode("primary", cfg.Primary); err != nil {
			return err
		}
		if len(cfg.Standbys) > 0 && cfg.Standby.Name != "" {
			return fmt.Errorf("use either standby (single) or standbys (cluster), not both")
		}
		standbys := cfg.ResolveStandbys()
		if len(standbys) == 0 {
			return fmt.Errorf("at least one standby is required for master-slave")
		}
		seenHosts := map[string]bool{cfg.Primary.Host: true}
		seenIDs := map[string]bool{cfg.Primary.ServerID: true}
		for i, s := range standbys {
			label := "standby"
			if len(standbys) > 1 {
				label = fmt.Sprintf("standbys[%d]", i)
			}
			if err := validateNode(label, s); err != nil {
				return err
			}
			if seenHosts[s.Host] {
				return fmt.Errorf("%s.host %q conflicts with another node", label, s.Host)
			}
			seenHosts[s.Host] = true
			if seenIDs[s.ServerID] {
				return fmt.Errorf("%s.server_id %q conflicts with another node", label, s.ServerID)
			}
			seenIDs[s.ServerID] = true
		}
	case "master-master":
		if err := validateNode("node_a", cfg.NodeA); err != nil {
			return err
		}
		if err := validateNode("node_b", cfg.NodeB); err != nil {
			return err
		}
		if cfg.NodeA.Host == cfg.NodeB.Host {
			return fmt.Errorf("node_a.host and node_b.host must be different")
		}
		if cfg.NodeA.ServerID == cfg.NodeB.ServerID {
			return fmt.Errorf("node_a.server_id and node_b.server_id must be different")
		}
		if strings.TrimSpace(cfg.Logical.Database) == "" {
			return fmt.Errorf("logical.database is required for master-master")
		}
		if strings.TrimSpace(cfg.Logical.PublicationA) == "" {
			return fmt.Errorf("logical.publication_a is required for master-master")
		}
		if strings.TrimSpace(cfg.Logical.PublicationB) == "" {
			return fmt.Errorf("logical.publication_b is required for master-master")
		}
		if strings.TrimSpace(cfg.Logical.SubscriptionA) == "" {
			return fmt.Errorf("logical.subscription_a is required for master-master")
		}
		if strings.TrimSpace(cfg.Logical.SubscriptionB) == "" {
			return fmt.Errorf("logical.subscription_b is required for master-master")
		}
		if strings.TrimSpace(cfg.Logical.ReplicationCIDR) == "" {
			return fmt.Errorf("logical.replication_cidr is required for master-master")
		}
		if _, err := netip.ParsePrefix(cfg.Logical.ReplicationCIDR); err != nil {
			return fmt.Errorf("logical.replication_cidr is invalid: %w", err)
		}
	default:
		return fmt.Errorf("mode must be master-slave or master-master")
	}

	if cfg.Failover.Enabled {
		if strings.ToLower(strings.TrimSpace(cfg.Mode)) != "master-slave" {
			return fmt.Errorf("failover.enabled is only supported for master-slave mode")
		}
		if cfg.Failover.CheckIntervalSec < 0 {
			return fmt.Errorf("failover.check_interval_sec must be non-negative")
		}
		if cfg.Failover.HealthTimeoutSec < 0 {
			return fmt.Errorf("failover.health_timeout_sec must be non-negative")
		}
		if cfg.Failover.MaxFailures < 1 {
			return fmt.Errorf("failover.max_failures must be at least 1")
		}
		if cfg.Failover.FenceTimeoutSec < 0 {
			return fmt.Errorf("failover.fence_timeout_sec must be non-negative")
		}
	}

	return nil
}

func validateNode(name string, node NodeConfig) error {
	if strings.TrimSpace(node.Name) == "" {
		return fmt.Errorf("%s.name is required", name)
	}
	if strings.TrimSpace(node.Host) == "" {
		return fmt.Errorf("%s.host is required", name)
	}
	if !isValidHost(node.Host) {
		return fmt.Errorf("%s.host is invalid", name)
	}
	if node.Port <= 0 || node.Port > 65535 {
		return fmt.Errorf("%s.port must be between 1 and 65535", name)
	}
	if strings.TrimSpace(node.DataDir) == "" {
		return fmt.Errorf("%s.data_dir is required", name)
	}
	if strings.TrimSpace(node.PostgresUser) == "" {
		return fmt.Errorf("%s.postgres_user is required", name)
	}
	if strings.TrimSpace(node.SSHUser) == "" {
		return fmt.Errorf("%s.ssh_user is required", name)
	}
	if strings.TrimSpace(node.ServerID) == "" {
		return fmt.Errorf("%s.server_id is required", name)
	}
	if strings.TrimSpace(node.DSN) == "" && strings.TrimSpace(node.DSNEnv) == "" {
		return fmt.Errorf("%s.dsn or %s.dsn_env is required", name, name)
	}
	if strings.TrimSpace(node.DSN) != "" {
		if _, err := pgx.ParseConfig(node.DSN); err != nil {
			return fmt.Errorf("%s.dsn is invalid: %w", name, err)
		}
	}

	return nil
}
