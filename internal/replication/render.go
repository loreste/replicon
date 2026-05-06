package replication

import (
	"fmt"
	"strings"
)

func RenderPlan(cfg Config) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Cluster: %s\n", cfg.ClusterName)
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "master-slave":
		standbys := cfg.ResolveStandbys()
		if len(standbys) == 1 {
			fmt.Fprintf(&b, "Topology: %s (%s:%d) -> %s (%s:%d)\n\n",
				cfg.Primary.Name, cfg.Primary.Host, cfg.Primary.Port,
				standbys[0].Name, standbys[0].Host, standbys[0].Port,
			)
			b.WriteString("Replication strategy: physical streaming replication with one hot standby.\n\n")
		} else {
			fmt.Fprintf(&b, "Topology: %s (%s:%d) -> [", cfg.Primary.Name, cfg.Primary.Host, cfg.Primary.Port)
			for i, s := range standbys {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s (%s:%d)", s.Name, s.Host, s.Port)
			}
			b.WriteString("]\n\n")
			fmt.Fprintf(&b, "Replication strategy: physical streaming replication with %d hot standbys.\n\n", len(standbys))
		}
		b.WriteString("Execution order:\n")
		b.WriteString("1. Apply the primary configuration snippet and reload PostgreSQL.\n")
		b.WriteString("2. Create the replication role and physical replication slot on the primary.\n")
		b.WriteString("3. Stop PostgreSQL on each standby and clear the data directory.\n")
		b.WriteString("4. Run pg_basebackup from each standby against the primary.\n")
		b.WriteString("5. Start PostgreSQL on each standby and verify pg_stat_replication on the primary.\n")
		b.WriteString("6. Run replicon verify -config replicon.json to confirm all standbys are replaying WAL.\n\n")
		b.WriteString("Rendered assets available:\n")
		b.WriteString("- replicon render -target primary\n")
		for _, s := range standbys {
			fmt.Fprintf(&b, "- replicon render -target %s\n", s.Name)
		}
	case "master-master":
		fmt.Fprintf(&b, "Topology: %s (%s:%d) <-> %s (%s:%d)\n\n",
			cfg.NodeA.Name, cfg.NodeA.Host, cfg.NodeA.Port,
			cfg.NodeB.Name, cfg.NodeB.Host, cfg.NodeB.Port,
		)
		b.WriteString("Replication strategy: logical bidirectional replication.\n")
		b.WriteString("Conflict note: this only works safely when the application avoids conflicting writes across both nodes.\n\n")
		b.WriteString("Execution order:\n")
		b.WriteString("1. Apply the logical replication configuration on both nodes.\n")
		b.WriteString("2. Create the replication role and pg_hba entries on both nodes.\n")
		b.WriteString("3. Create publications on both nodes.\n")
		b.WriteString("4. Create cross-subscriptions so each node subscribes to the other.\n")
		b.WriteString("5. Run replicon verify -config replicon.json to confirm both subscriptions are healthy.\n\n")
		b.WriteString("Rendered assets available:\n")
		b.WriteString("- replicon render -target node-a\n")
		b.WriteString("- replicon render -target node-b\n")
	}

	return b.String()
}

func RenderTarget(cfg Config, target string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "master-slave":
		targetLower := strings.ToLower(strings.TrimSpace(target))
		if targetLower == "primary" {
			return renderPrimary(cfg), nil
		}
		if targetLower == "standby" {
			return renderStandbyNode(cfg, cfg.Standby), nil
		}
		// Cluster mode: match by standby name.
		for _, s := range cfg.Standbys {
			if strings.ToLower(s.Name) == targetLower {
				return renderStandbyNode(cfg, s), nil
			}
		}
	case "master-master":
		switch strings.ToLower(strings.TrimSpace(target)) {
		case "node-a":
			return renderLogicalNodeA(cfg), nil
		case "node-b":
			return renderLogicalNodeB(cfg), nil
		}
	}

	return "", fmt.Errorf("unknown render target %q for mode %q", target, cfg.Mode)
}

func renderPrimary(cfg Config) string {
	return fmt.Sprintf(`# Primary configuration for %s

## postgresql.conf
listen_addresses = '*'
port = %d
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
archive_mode = on
archive_command = 'test ! -f /var/lib/postgresql/archive/%%f && cp %%p /var/lib/postgresql/archive/%%f'

## pg_hba.conf
host replication %s %s scram-sha-256

## Secret handling
export REPL_PASSWORD='replace-me'

## SQL to run on primary
CREATE ROLE %s WITH REPLICATION LOGIN PASSWORD '${REPL_PASSWORD}';
SELECT * FROM pg_create_physical_replication_slot('%s');

## Verification query
SELECT application_name, client_addr, state, sync_state
FROM pg_stat_replication;
`,
		cfg.ClusterName,
		cfg.Primary.Port,
		cfg.ReplicationUser,
		cfg.Network.ReplicationCIDR,
		cfg.ReplicationUser,
		cfg.ReplicationSlot,
	)
}

func renderStandbyNode(cfg Config, standby NodeConfig) string {
	return fmt.Sprintf(`# Standby bootstrap for %s

## Stop PostgreSQL on standby before running this
sudo systemctl stop postgresql

## Secret handling
export REPL_PASSWORD='replace-me'

## Recreate standby from the primary
rm -rf %[1]s/*
PGPASSWORD="$REPL_PASSWORD" pg_basebackup \
  --pgdata='%[1]s' \
  --write-recovery-conf \
  --slot='%[2]s' \
  --host='%[3]s' \
  --port='%[4]d' \
  --username='%[5]s' \
  --checkpoint=fast \
  --create-slot \
  --progress

## postgresql.auto.conf additions
primary_conninfo = 'host=%[3]s port=%[4]d user=%[5]s password=${REPL_PASSWORD} application_name=%[6]s'
primary_slot_name = '%[2]s'
hot_standby = on

## Start and verify
sudo systemctl start postgresql
psql -c "SELECT pg_is_in_recovery();"
`,
		standby.DataDir,
		cfg.ReplicationSlot,
		cfg.Primary.Host,
		cfg.Primary.Port,
		cfg.ReplicationUser,
		cfg.Network.ApplicationName,
	)
}

func renderLogicalNodeA(cfg Config) string {
	return fmt.Sprintf(`# Logical replication setup for node-a (%s)

## postgresql.conf
listen_addresses = '*'
port = %d
wal_level = logical
max_wal_senders = 10
max_replication_slots = 10

## pg_hba.conf
host replication %s %s scram-sha-256
host %s %s scram-sha-256

## Secret handling
export REPL_PASSWORD='replace-me'

## SQL on node-a
CREATE ROLE %s WITH REPLICATION LOGIN PASSWORD '${REPL_PASSWORD}';
CREATE PUBLICATION %s FOR ALL TABLES;
CREATE SUBSCRIPTION %s
CONNECTION 'host=%s port=%d dbname=%s user=%s password=${REPL_PASSWORD} application_name=%s'
PUBLICATION %s
WITH (copy_data = false, create_slot = true, enabled = true, origin = none);

## Verification query
SELECT subname, coalesce(status, 'unknown'), received_lsn, latest_end_lsn
FROM pg_stat_subscription;
`,
		cfg.NodeA.Name,
		cfg.NodeA.Port,
		cfg.ReplicationUser,
		cfg.Logical.ReplicationCIDR,
		cfg.Logical.Database,
		cfg.Logical.ReplicationCIDR,
		cfg.ReplicationUser,
		cfg.Logical.PublicationA,
		cfg.Logical.SubscriptionA,
		cfg.NodeB.Host,
		cfg.NodeB.Port,
		cfg.Logical.Database,
		cfg.ReplicationUser,
		cfg.NodeA.Name,
		cfg.Logical.PublicationB,
	)
}

func renderLogicalNodeB(cfg Config) string {
	return fmt.Sprintf(`# Logical replication setup for node-b (%s)

## postgresql.conf
listen_addresses = '*'
port = %d
wal_level = logical
max_wal_senders = 10
max_replication_slots = 10

## pg_hba.conf
host replication %s %s scram-sha-256
host %s %s scram-sha-256

## Secret handling
export REPL_PASSWORD='replace-me'

## SQL on node-b
CREATE ROLE %s WITH REPLICATION LOGIN PASSWORD '${REPL_PASSWORD}';
CREATE PUBLICATION %s FOR ALL TABLES;
CREATE SUBSCRIPTION %s
CONNECTION 'host=%s port=%d dbname=%s user=%s password=${REPL_PASSWORD} application_name=%s'
PUBLICATION %s
WITH (copy_data = false, create_slot = true, enabled = true, origin = none);

## Verification query
SELECT subname, coalesce(status, 'unknown'), received_lsn, latest_end_lsn
FROM pg_stat_subscription;
`,
		cfg.NodeB.Name,
		cfg.NodeB.Port,
		cfg.ReplicationUser,
		cfg.Logical.ReplicationCIDR,
		cfg.Logical.Database,
		cfg.Logical.ReplicationCIDR,
		cfg.ReplicationUser,
		cfg.Logical.PublicationB,
		cfg.Logical.SubscriptionB,
		cfg.NodeA.Host,
		cfg.NodeA.Port,
		cfg.Logical.Database,
		cfg.ReplicationUser,
		cfg.NodeB.Name,
		cfg.Logical.PublicationA,
	)
}
