# replicon

A Go CLI and API for managing PostgreSQL replication — setup, verification, failover, and monitoring across master-slave, cluster, and master-master topologies.

## Why this exists

PostgreSQL replication works well once configured. The problem is getting there and staying confident it's working.

**Setup is manual and error-prone.** A primary-standby pair requires coordinated changes across `postgresql.conf`, `pg_hba.conf`, replication roles, replication slots, `pg_basebackup`, and recovery parameters. Each step depends on the previous one being correct, and mistakes fail silently — a wrong CIDR block means the standby just never connects. For logical replication the surface area doubles: both nodes need matching schemas, publications, cross-subscriptions, and the `origin = none` flag to prevent infinite loops.

**There is no built-in way to confirm data is actually flowing.** `pg_stat_replication` shows a connection exists. It does not prove rows are replicating. A subscription can show `streaming` in `pg_stat_subscription` while the apply worker is stuck. The only proof is writing data on one side and confirming it appears on the other.

**Failover under pressure is where mistakes happen.** `pg_promote()` is simple, but the steps around it — fencing the old primary, choosing the best standby in a cluster, rebuilding the old primary as a standby — are easy to get wrong when you're in an incident.

**Configuration is scattered.** Replication settings live across multiple files and PostgreSQL catalog tables. There is no single source of truth for what the topology should look like.

### How replicon compares

| Tool | What it does | When to use it instead of replicon |
|------|-------------|-------------------------------------|
| **Patroni** / **Stolon** | Full HA orchestrators with leader election via etcd/ZooKeeper/Consul. Manage the entire PostgreSQL lifecycle. | You need consensus-based automatic failover across large clusters and are willing to run a DCS. |
| **repmgr** | Replication manager with witness nodes and daemon-based monitoring. | You want a mature, well-documented replication manager with its own daemon process. |
| **pg_basebackup** | Low-level PostgreSQL tool for creating base backups. | You only need the backup step and will script everything else yourself. |
| **pgBackRest** / **Barman** | Backup and point-in-time recovery. | Your focus is backup management, not replication topology. |

replicon fills a different space. It is a single static binary that:

- defines your topology in one version-controlled JSON file
- validates the configuration before you touch any server
- renders the exact `postgresql.conf`, `pg_hba.conf`, and SQL you need to apply
- verifies replication is active by querying PostgreSQL system views
- proves data is flowing with an active write/read/delete probe
- manages failover with dry-run first, execute when ready
- automatically fails over with fence-then-promote safety when configured
- selects the best standby for promotion in multi-standby clusters
- exposes a TLS API with audit logging and Prometheus metrics for service mode

It does not require etcd, ZooKeeper, Consul, or any external dependency beyond PostgreSQL and SSH.

## What replicon supports

| Capability | Status |
|------------|--------|
| Master-slave (1 primary + 1 standby) | Tested on PG 13, 14, 16 |
| Cluster (1 primary + N standbys) | Tested on PG 16; best-standby promotion by WAL position |
| Master-master (2 writable nodes, logical replication) | Tested on PG 16; bidirectional probe confirmed |
| Configuration validation | JSON schema checks, DSN parsing, CIDR validation, node uniqueness |
| Setup rendering | Generates `postgresql.conf`, `pg_hba.conf`, SQL, and `pg_basebackup` commands |
| Read-only verification | Queries `pg_stat_replication`, `pg_stat_subscription`, standby recovery state |
| Active end-to-end probe | Writes a row, waits for replication, deletes it, confirms deletion replicates |
| Manual failover (dry-run + execute) | `promote` and `rejoin` with SSH execution and preflight checks |
| Automatic failover | `watch` command: health monitoring, fence-then-promote, split-brain prevention |
| Cluster-aware promotion | Queries all standbys' WAL receive LSN, promotes the most up-to-date one |
| TLS admin API | `/verify`, `/probe`, `/promote`, `/rejoin`, `/metrics`, `/history` endpoints |
| Prometheus metrics | Command run counts and durations, scrapeable at `/metrics` |
| Audit logging | Every operation recorded in append-only JSONL with credential redaction |
| SSH preflight | Validates SSH connectivity to all nodes before destructive operations |
| Cross-platform | Static binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 |
| No runtime dependencies | Single binary. No etcd, no ZooKeeper, no Consul, no agent on the PG servers. |

### What replicon does not do

- **Leader election via distributed consensus.** The `watch` command uses fence-then-promote: it stops the old primary via SSH before promoting. If it cannot fence (SSH also fails), it does not promote, to prevent split-brain. This is a deliberate trade-off — it avoids the complexity of a DCS at the cost of not promoting during certain network partitions.
- **DDL replication for master-master.** This is a PostgreSQL limitation. Schemas and tables must exist on both nodes before data can flow.
- **Conflict resolution for master-master writes.** The application must partition writes (by region, tenant, or dataset) to avoid conflicts.

## Architecture

Master-slave / cluster:

```mermaid
flowchart LR
    A[replicon] --> B[Primary]
    A --> C[Standby 1]
    A --> D[Standby 2]
    A --> E[Standby N]
    B -->|WAL streaming| C
    B -->|WAL streaming| D
    B -->|WAL streaming| E
    A -->|verify| F[pg_stat_replication]
    A -->|probe write| B
    A -->|probe read| C
    A -->|probe read| D
    A -->|watch| G[health check → fence → promote best]
```

Master-master:

```mermaid
flowchart LR
    A[replicon] --> B[Node A]
    A --> C[Node B]
    B -->|Logical pub/sub| C
    C -->|Logical pub/sub| B
    A -->|verify| D[pg_stat_subscription]
    A -->|probe write/read/delete| B
    A -->|probe write/read/delete| C
```

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

### Cluster (multiple standbys)

Use the `standbys` array instead of the single `standby` field:

```json
{
  "cluster_name": "orders-prod",
  "mode": "master-slave",
  "replication_user": "replicator",
  "replication_slot": "orders_prod_standby",
  "primary": {
    "name": "primary",
    "host": "10.0.0.10",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-a",
    "dsn_env": "REPLICON_PRIMARY_DSN"
  },
  "standbys": [
    {
      "name": "standby-1",
      "host": "10.0.0.11",
      "port": 5432,
      "data_dir": "/var/lib/postgresql/16/main",
      "postgres_user": "postgres",
      "ssh_user": "ubuntu",
      "server_id": "pg-b",
      "dsn_env": "REPLICON_STANDBY_1_DSN"
    },
    {
      "name": "standby-2",
      "host": "10.0.0.12",
      "port": 5432,
      "data_dir": "/var/lib/postgresql/16/main",
      "postgres_user": "postgres",
      "ssh_user": "ubuntu",
      "server_id": "pg-c",
      "dsn_env": "REPLICON_STANDBY_2_DSN"
    }
  ],
  "network": {
    "replication_cidr": "10.0.0.0/24",
    "application_name": "orders-prod-standby"
  }
}
```

`verify` and `probe` check all standbys. `promote` queries each standby's WAL receive position and promotes the one closest to the primary. Do not use both `standby` and `standbys`.

## Failover

### Manual (dry-run first)

```bash
replicon promote -config replicon.json           # shows what would happen
replicon promote -config replicon.json -execute   # runs it over SSH
replicon rejoin -config replicon.json -execute    # rebuilds old primary as standby
```

### Automatic

Add a `failover` section to your config:

```json
{
  "failover": {
    "enabled": true,
    "check_interval_sec": 5,
    "health_timeout_sec": 3,
    "max_failures": 3,
    "fence_timeout_sec": 10,
    "fence_command": "sudo systemctl stop postgresql",
    "post_promote_command": ""
  }
}
```

Start the watchdog:

```bash
replicon watch -config replicon.json -audit-log var/audit/replicon.jsonl
```

The watchdog:

1. Checks primary health every `check_interval_sec` via SQL connection
2. After `max_failures` consecutive failures, fences the primary via SSH
3. If fencing succeeds, promotes the best standby (by WAL position in cluster mode)
4. If fencing fails, does **not** promote — prevents split-brain
5. Optionally runs `post_promote_command` on the new primary

All events are recorded in the audit log.

| Field | Default | Description |
|-------|---------|-------------|
| `check_interval_sec` | 5 | Seconds between health checks |
| `health_timeout_sec` | 3 | Timeout for each SQL health check |
| `max_failures` | 3 | Consecutive failures before failover |
| `fence_timeout_sec` | 10 | Timeout for the SSH fence command |
| `fence_command` | `sudo systemctl stop postgresql` | Command to stop PostgreSQL on the primary |
| `post_promote_command` | _(none)_ | Optional command on new primary after promotion |

## Service Mode

Run as a TLS-protected API:

```bash
export REPLICON_API_KEY='replace-with-long-random-token'
replicon serve \
  -config replicon.json \
  -listen :8443 \
  -tls-cert server.crt \
  -tls-key server.key \
  -audit-log var/audit/replicon.jsonl
```

Endpoints:

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/healthz` | GET | No | Process liveness |
| `/readyz` | GET | Yes | Config validation |
| `/metrics` | GET | Yes | Prometheus counters |
| `/api/v1/validate` | GET/POST | Yes | Validate config |
| `/api/v1/verify` | GET/POST | Yes | Check replication state |
| `/api/v1/probe` | GET/POST | Yes | Active replication test |
| `/api/v1/promote` | POST | Yes | Promote standby (with preflight) |
| `/api/v1/rejoin` | POST | Yes | Rejoin old primary (with preflight) |
| `/api/v1/history` | GET | Yes | Recent audit entries |

## Commands

```
replicon init [-mode master-slave|master-master]
replicon validate -config <file> [-output text|json] [-audit-log path]
replicon plan -config <file>
replicon render -config <file> -target <primary|standby|node-a|node-b>
replicon verify -config <file> [-output text|json] [-audit-log path]
replicon probe -config <file> [-output text|json] [-audit-log path]
replicon promote -config <file> [-execute] [-skip-preflight] [-output text|json]
replicon rejoin -config <file> [-execute] [-skip-preflight] [-output text|json]
replicon preflight -config <file> [-output text|json]
replicon watch -config <file> [-audit-log path]
replicon history [-audit-log path] [-limit 20] [-output text|json]
replicon serve -config <file> -tls-cert <cert> -tls-key <key> [-listen :8080]
```

## Config Notes

- `dsn_env` is the recommended way to provide credentials. `dsn` works but puts connection strings in the config file.
- Rendered setup snippets use `REPL_PASSWORD` shell variable placeholders.
- `probe` writes to `public.replicon_replication_probe` — the DSN user needs CREATE and DML permissions.
- `promote` and `rejoin` use SSH for remote execution. Run `preflight` first to verify connectivity.
- Use `standbys` (array) for clusters with multiple standbys, or `standby` (object) for a single standby. Do not use both.

## Documentation

- [Linux Installation And Configuration](./docs/linux-setup.md) — complete step-by-step for bare-metal and VM servers
- [Installation](./docs/installation.md) — build from source, binary install, Docker, systemd
- [Master-Slave Setup](./docs/master-slave.md)
- [Master-Master Setup](./docs/master-master.md)
- [Verification And Probing](./docs/verification.md)
- [Service Mode](./docs/service-mode.md)
- [Deployment](./docs/deployment.md)
- [Integration Environment](./integration/README.md)

## Development

```bash
make test           # unit tests
make test-race      # unit + stress tests with race detector
make test-integration  # integration tests against live PostgreSQL
make test-all       # lint + race + integration
make bench          # allocation benchmarks
make build          # build binary to bin/replicon
make docker-build   # build container image
make package-release # cross-platform static binaries
```

CI: [ci.yml](./.github/workflows/ci.yml) | Release: [release.yml](./.github/workflows/release.yml)

## Limitations

- Automatic failover uses fence-then-promote, not distributed consensus. If SSH to the primary also fails during an outage, the watchdog will not promote. This prevents split-brain but means some failure scenarios require manual intervention.
- Master-master logical replication does not replicate DDL. Schemas and tables must exist on both nodes.
- Master-master does not resolve write conflicts. The application must partition writes to avoid conflicts.
- replicon manages a single primary. It does not support multi-primary setups beyond two-node logical replication.
