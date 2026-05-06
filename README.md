# replicon

A Go CLI and API for managing PostgreSQL replication — setup, verification, failover, and monitoring.

## Why this exists

PostgreSQL replication works well once configured. The problem is getting there and staying confident it's working.

**Setup is manual and error-prone.** A primary-standby pair requires coordinated changes across `postgresql.conf`, `pg_hba.conf`, replication roles, replication slots, `pg_basebackup`, and recovery parameters. Each step depends on the previous one being correct, and mistakes fail silently. For logical replication the surface area doubles: both nodes need matching schemas, publications, cross-subscriptions, and the `origin = none` flag to prevent infinite loops.

**There is no built-in way to confirm data is actually flowing.** `pg_stat_replication` shows a connection exists, not that rows are replicating. The only proof is writing data on one side and confirming it appears on the other.

**Failover under pressure is where mistakes happen.** `pg_promote()` is simple, but the steps around it — fencing the old primary, choosing the best standby, rebuilding the old primary as a standby — are easy to get wrong in an incident.

**Configuration is scattered.** Replication settings live across multiple files and PostgreSQL catalog tables with no single source of truth.

### How replicon compares

| Tool | What it does | When to use it instead |
|------|-------------|------------------------|
| **Patroni** / **Stolon** | Full HA orchestrators with Raft-based leader election via etcd/ZooKeeper/Consul | You need formal distributed consensus guarantees and are willing to run a DCS |
| **repmgr** | Replication manager with witness nodes and daemon-based monitoring | You want a mature, battle-tested replication manager |
| **pgBackRest** / **Barman** | Backup and point-in-time recovery | Your focus is backup management, not replication topology |

replicon is a single static binary with no external dependencies beyond PostgreSQL and SSH.

## Capabilities

### Topology modes

| Mode | Replication type | What replicon does |
|------|-----------------|-------------------|
| **Master-slave** (1 primary + 1 standby) | Physical streaming (WAL). Replicates everything — all databases, schemas, tables, roles. | `verify`, `probe`, `promote`, `rejoin`, `watch` — full lifecycle. Tested on PG 13, 14, 16. |
| **Cluster** (1 primary + N standbys) | Physical streaming (WAL). Same as master-slave but with multiple standbys. | `verify` and `probe` check all standbys. `promote` selects the standby with the highest WAL receive LSN. Tested on PG 16. |
| **Master-master** (2 writable nodes) | Logical (row-level, single database). Does not replicate DDL or other databases. | `verify`, `probe` (bidirectional), `ddl-setup`, `ddl-sync`, `conflicts`. Tested on PG 16. |
| **Multi-node logical** (N writable nodes) | Logical. Same caveats as master-master. | `ddl-sync` and `conflicts` work across N nodes via `logical.nodes`. `verify` and `probe` only check two-node `node_a`/`node_b` pairs — multi-node verify is not yet implemented. |

### Failover

| Capability | How it works |
|------------|-------------|
| **Manual failover** | `promote` (dry-run by default, `-execute` to run over SSH) and `rejoin` (rebuilds old primary as standby) |
| **Automatic failover** | `watch` monitors the primary, fences it via SSH after consecutive failures, promotes the best standby |
| **Witness node** | When SSH fencing fails, a witness PostgreSQL instance on a third host independently checks if the primary is reachable. If both the watchdog and witness agree it's down, promotion proceeds without fencing. If the witness can still reach the primary, promotion is aborted (likely a network partition). This is not distributed consensus — it reduces split-brain risk but does not eliminate it. |
| **Leader election** | Multiple `watch` agents can run against the same cluster. A coordination PostgreSQL database tracks which agent holds the leader lease. Only the leader triggers failover. If the leader stops heartbeating, another agent takes over after the lease TTL expires. Uses row-level locking and TTL — not Raft or Paxos. |
| **Dry-run** | `watch -dry-run` monitors and logs what would happen without fencing or promoting |
| **Cluster-aware** | In multi-standby configs, `promote` queries each standby's WAL receive position and promotes the most up-to-date one |

### Master-master extras

| Capability | How it works | Caveats |
|------------|-------------|---------|
| **DDL tracking** | `ddl-setup` installs PostgreSQL event triggers that capture DDL (CREATE/ALTER/DROP for tables, indexes, sequences, views, types, functions, schemas) into a tracking table | Not real-time. Run `ddl-sync` manually or on a cron. Complex data-dependent DDL may fail during replay. |
| **DDL sync** | `ddl-sync` reads unreplayed DDL from each node and replays it on all other nodes | Disables the trigger during replay to avoid loops. Marks entries as replayed. |
| **Conflict detection** | `conflicts` checks all nodes for stalled subscription workers and recent conflict log entries | Detection only — cannot prevent conflicts |
| **Conflict handling** | `skip` strategy advances past the conflicting transaction (lossy). `log` strategy records and stalls. `last_write_wins` is documented as an application-level pattern — replicon does not enforce it. | PostgreSQL logical replication has no hook for custom conflict handlers |

### Operations and observability

| Capability | Details |
|------------|---------|
| **Config validation** | DSN parsing, CIDR validation, node uniqueness, mode-specific field checks |
| **Setup rendering** | Generates `postgresql.conf`, `pg_hba.conf`, SQL, and `pg_basebackup` commands per node |
| **Read-only verification** | Queries `pg_stat_replication` and `pg_stat_subscription` |
| **Active probe** | Writes a row, waits for replication, deletes it, confirms deletion replicates |
| **SSH preflight** | Validates connectivity to all nodes before destructive operations |
| **TLS admin API** | `/verify`, `/probe`, `/promote`, `/rejoin`, `/metrics`, `/history` endpoints with API key auth |
| **Prometheus metrics** | Command run counts and durations at `/metrics` |
| **Audit logging** | Append-only JSONL with credential redaction |
| **Cross-platform** | Static binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 |

## Quick Start

### Master-slave

```bash
export REPLICON_PRIMARY_DSN='postgres://postgres:secret@10.0.0.10:5432/postgres?sslmode=require'
export REPLICON_STANDBY_DSN='postgres://postgres:secret@10.0.0.11:5432/postgres?sslmode=require'

replicon init -mode master-slave > replicon.json
replicon validate -config replicon.json
replicon plan -config replicon.json
replicon render -config replicon.json -target primary
replicon render -config replicon.json -target standby
replicon verify -config replicon.json
replicon probe -config replicon.json
```

### Master-master

```bash
export REPLICON_NODE_A_DSN='postgres://postgres:secret@10.0.0.10:5432/appdb?sslmode=require'
export REPLICON_NODE_B_DSN='postgres://postgres:secret@10.0.0.11:5432/appdb?sslmode=require'

replicon init -mode master-master > replicon-mm.json
replicon validate -config replicon-mm.json
replicon render -config replicon-mm.json -target node-a
replicon render -config replicon-mm.json -target node-b
replicon verify -config replicon-mm.json
replicon probe -config replicon-mm.json
```

## Failover

### Manual

```bash
replicon promote -config replicon.json           # dry-run: shows commands
replicon promote -config replicon.json -execute   # runs over SSH
replicon rejoin -config replicon.json -execute    # rebuilds old primary as standby
```

### Automatic

```json
{
  "failover": {
    "enabled": true,
    "check_interval_sec": 5,
    "health_timeout_sec": 3,
    "max_failures": 3,
    "fence_timeout_sec": 10,
    "fence_command": "sudo systemctl stop postgresql",
    "post_promote_command": "",
    "witness": {
      "enabled": false,
      "dsn_env": "REPLICON_WITNESS_DSN"
    },
    "election": {
      "enabled": false,
      "dsn_env": "REPLICON_ELECTION_DSN",
      "node_id": "agent-1",
      "lease_ttl_sec": 30,
      "renew_sec": 10
    }
  }
}
```

```bash
replicon watch -config replicon.json -audit-log var/audit/replicon.jsonl
replicon watch -config replicon.json -dry-run    # monitor without acting
```

**Watchdog flow:**

1. Check primary health every `check_interval_sec` via SQL
2. After `max_failures` consecutive failures, fence the primary via SSH
3. If fencing succeeds, promote the best standby
4. If fencing fails and a witness is configured, consult the witness before deciding
5. If leader election is enabled, only the elected leader triggers failover
6. All events recorded in the audit log

**Leader election flow:**

1. Each agent tries to acquire a lease row in a coordination PostgreSQL database
2. The lease holder is the leader — the only agent that can trigger failover
3. The leader renews the lease every `renew_sec`
4. If the leader stops renewing, the lease expires after `lease_ttl_sec` and another agent takes over
5. On graceful shutdown, the leader voluntarily expires its lease

## Commands

```
replicon init [-mode master-slave|master-master]
replicon validate -config <file> [-output text|json] [-audit-log path]
replicon plan -config <file>
replicon render -config <file> -target <primary|standby|standby-name|node-a|node-b>
replicon verify -config <file> [-output text|json] [-audit-log path]
replicon probe -config <file> [-output text|json] [-audit-log path]
replicon promote -config <file> [-execute] [-skip-preflight] [-output text|json]
replicon rejoin -config <file> [-execute] [-skip-preflight] [-output text|json]
replicon preflight -config <file> [-output text|json]
replicon watch -config <file> [-audit-log path] [-dry-run]
replicon ddl-setup -config <file> [-output text|json]
replicon ddl-sync -config <file> [-output text|json]
replicon conflicts -config <file> [-output text|json]
replicon history [-audit-log path] [-limit 20] [-output text|json]
replicon serve -config <file> -tls-cert <cert> -tls-key <key> [-listen :8080]
replicon version
```

## Config

- `dsn_env` is the recommended way to provide credentials. `dsn` works but puts connection strings in the config file.
- Use `standbys` (array) for clusters, or `standby` (object) for a single standby. Do not use both.
- Use `logical.nodes` (array) for multi-node logical replication, or `node_a`/`node_b` for two-node.
- The `.pgpass` file on the old primary is required for `rejoin`. See [deploy/pgpass.example](./deploy/pgpass.example).
- `probe` writes to `public.replicon_replication_probe` — the DSN user needs CREATE and DML permissions.

## Documentation

- [Linux Installation And Configuration](./docs/linux-setup.md)
- [Installation](./docs/installation.md)
- [Master-Slave Setup](./docs/master-slave.md)
- [Master-Master Setup](./docs/master-master.md)
- [Verification And Probing](./docs/verification.md)
- [Service Mode](./docs/service-mode.md)
- [Deployment](./docs/deployment.md)
- [Integration Environment](./integration/README.md)

## Development

```bash
make test              # unit tests
make test-race         # unit + stress tests with race detector
make test-integration  # integration tests against live PostgreSQL
make test-all          # lint + race + integration
make bench             # allocation benchmarks
make build             # build binary with version info
make package-release   # cross-platform static binaries
```

## Limitations

- **Leader election uses PostgreSQL row locking, not Raft/Paxos.** It handles agent crashes, network blips, and rolling restarts. It does not provide the formal consensus guarantees of etcd or ZooKeeper. If the coordination database goes down, election stalls until it recovers.
- **Witness-based failover is not distributed consensus.** Two observers agreeing reduces split-brain risk but does not eliminate it. Both could be on the wrong side of a partition.
- **Multi-node logical `verify` and `probe` are two-node only.** `ddl-sync` and `conflicts` work across N nodes, but `verify` and `probe` only check `node_a`/`node_b`.
- **DDL sync is not real-time.** There is a delay between DDL execution and replay. Complex DDL may fail during replay.
- **Conflict resolution is limited.** `skip` is lossy. `last_write_wins` is an application pattern, not enforced by replicon. PostgreSQL has no hook for custom conflict handlers in logical replication.
- **Master-master requires write partitioning.** replicon detects and logs conflicts but cannot prevent them.

## License

[MIT](./LICENSE)
