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

replicon fills a different space. It is a single static binary with no external dependencies beyond PostgreSQL and SSH. No etcd, no ZooKeeper, no Consul, no agent on the database servers.

## What replicon supports

| Capability | Status |
|------------|--------|
| **Master-slave** (1 primary + 1 standby) | Tested on PG 13, 14, 16 |
| **Cluster** (1 primary + N standbys) | Tested on PG 16; best-standby promotion by WAL position |
| **Master-master** (2 writable nodes, logical replication) | Tested on PG 16; bidirectional probe confirmed |
| **Multi-node logical replication** (N writable nodes) | DDL sync and conflict detection work across N nodes; verify and probe currently check two nodes |
| Configuration validation | JSON schema checks, DSN parsing, CIDR validation, node uniqueness |
| Setup rendering | Generates `postgresql.conf`, `pg_hba.conf`, SQL, and `pg_basebackup` commands |
| Read-only verification | Queries `pg_stat_replication`, `pg_stat_subscription`, standby recovery state |
| Active end-to-end probe | Writes a row, waits for replication, deletes it, confirms deletion replicates |
| Manual failover (dry-run + execute) | `promote` and `rejoin` with SSH execution and preflight checks |
| Automatic failover | `watch` command with health monitoring, fence-then-promote, and dry-run mode |
| Witness-based failover | When SSH fencing fails, a witness node independently confirms the primary is down before promoting |
| Cluster-aware promotion | Queries all standbys' WAL receive LSN, promotes the most up-to-date one |
| DDL tracking and sync | Event trigger capture and cross-node replay for master-master (`ddl-setup`, `ddl-sync`) |
| Conflict detection | Checks for stalled subscriptions and logs conflicts across all nodes |
| Conflict handling | Configurable strategies: `skip` (advance past conflict), `log` (record and stall) |
| TLS admin API | `/verify`, `/probe`, `/promote`, `/rejoin`, `/metrics`, `/history` endpoints |
| Prometheus metrics | Command run counts and durations, scrapeable at `/metrics` |
| Audit logging | Every operation recorded in append-only JSONL with credential redaction |
| SSH preflight | Validates SSH connectivity to all nodes before destructive operations |
| Cross-platform | Static binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 |

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
    A -->|watch| G[health check -> fence -> promote best]
```

Master-master / multi-node:

```mermaid
flowchart LR
    A[replicon] --> B[Node 1]
    A --> C[Node 2]
    A --> D[Node N]
    B -->|Logical pub/sub| C
    B -->|Logical pub/sub| D
    C -->|Logical pub/sub| B
    C -->|Logical pub/sub| D
    D -->|Logical pub/sub| B
    D -->|Logical pub/sub| C
    A -->|verify| E[pg_stat_subscription]
    A -->|ddl-sync| F[DDL capture and replay]
    A -->|conflicts| G[conflict detection]
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

### Multi-node logical replication

For more than two writable nodes, use the `logical.nodes` array:

```json
{
  "cluster_name": "orders-global",
  "mode": "master-master",
  "replication_user": "replicator",
  "logical": {
    "database": "appdb",
    "replication_cidr": "10.0.0.0/24",
    "nodes": [
      {
        "name": "us-east",
        "host": "10.0.0.10",
        "port": 5432,
        "data_dir": "/var/lib/postgresql/16/main",
        "postgres_user": "postgres",
        "ssh_user": "ubuntu",
        "server_id": "pg-us-east",
        "dsn_env": "REPLICON_US_EAST_DSN"
      },
      {
        "name": "us-west",
        "host": "10.0.0.11",
        "port": 5432,
        "data_dir": "/var/lib/postgresql/16/main",
        "postgres_user": "postgres",
        "ssh_user": "ubuntu",
        "server_id": "pg-us-west",
        "dsn_env": "REPLICON_US_WEST_DSN"
      },
      {
        "name": "eu-central",
        "host": "10.0.0.12",
        "port": 5432,
        "data_dir": "/var/lib/postgresql/16/main",
        "postgres_user": "postgres",
        "ssh_user": "ubuntu",
        "server_id": "pg-eu",
        "dsn_env": "REPLICON_EU_DSN"
      }
    ],
    "conflict_resolution": {
      "strategy": "last_write_wins"
    },
    "ddl_replication": {
      "enabled": true
    }
  }
}
```

Each node publishes to and subscribes from every other node. Publications, subscriptions, and `pg_hba.conf` entries must be created manually on each node — replicon does not automate the initial mesh setup. Once the mesh is running, `ddl-sync` and `conflicts` operate across all nodes. `verify` and `probe` currently work with the two-node `node_a`/`node_b` configuration; multi-node verification is not yet implemented.

The application must partition writes to avoid conflicts. Existing two-node configs with `node_a`/`node_b` continue to work unchanged.

## Failover

### Manual (dry-run first)

```bash
replicon promote -config replicon.json           # shows what would happen
replicon promote -config replicon.json -execute   # runs it over SSH
replicon rejoin -config replicon.json -execute    # rebuilds old primary as standby
```

In cluster mode, `promote` automatically selects the standby with the most recent WAL receive position.

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

Test the watchdog without risking a real failover:

```bash
replicon watch -config replicon.json -dry-run
```

The watchdog:

1. Checks primary health every `check_interval_sec` via SQL connection
2. After `max_failures` consecutive failures, fences the primary via SSH
3. If fencing succeeds, promotes the best standby (by WAL position in cluster mode)
4. If fencing fails, consults the witness node (if configured) before deciding
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

### Witness node

When SSH to the primary also fails during an outage, the watchdog cannot fence and would normally abort promotion. A witness node solves this by providing a second independent observer.

```json
{
  "failover": {
    "enabled": true,
    "max_failures": 3,
    "witness": {
      "enabled": true,
      "dsn_env": "REPLICON_WITNESS_DSN"
    }
  }
}
```

The witness is a PostgreSQL instance on a third host (can be a small instance — it is only used for health checks). When fencing fails:

1. The watchdog connects to the witness database
2. If the `dblink` extension is installed on the witness, it attempts to reach the primary from the witness's network. If dblink says the primary is still alive, promotion is aborted — the outage is a network partition, not a real failure
3. If `dblink` is not available, the watchdog uses two-observer agreement: the watchdog can't reach the primary AND the witness is alive and healthy, so two independent observers on different networks both see the primary as unreachable
4. If both agree the primary is down, promotion proceeds without fencing
5. If the witness is also unreachable, the watchdog does not promote — not enough information to decide

This is not equivalent to distributed consensus. It reduces the risk of split-brain but does not eliminate it entirely. For the strongest guarantees, use a DCS-based orchestrator like Patroni.

## DDL Tracking and Sync

PostgreSQL logical replication does not replicate DDL (CREATE TABLE, ALTER TABLE, etc.). replicon provides a capture-and-replay mechanism using PostgreSQL event triggers. This is not real-time — DDL is captured as it happens, but replay to other nodes is triggered manually or on a schedule.

### Setup

Install DDL tracking on all master-master nodes:

```bash
replicon ddl-setup -config replicon.json
```

This creates:

- A `replicon_ddl_log` tracking table on each node
- An event trigger (`replicon_ddl_capture`) that records DDL statements as they execute
- Captures CREATE, ALTER, and DROP for tables, indexes, sequences, views, types, functions, and schemas

### Sync

Replay pending DDL from each node to all other nodes:

```bash
replicon ddl-sync -config replicon.json
```

This:

1. Reads unreplayed DDL entries from each node's tracking table
2. Disables the event trigger on the target to avoid re-capturing during replay
3. Executes each DDL statement on the target
4. Re-enables the trigger and marks entries as replayed

Run `ddl-sync` after making schema changes, or schedule it on a cron for near-automated operation. This is not instant — there is a window between DDL execution on the source and replay on other nodes. Complex DDL that depends on data (e.g. `ALTER TABLE ... USING` with data transformations) may fail during replay if the data differs across nodes.

## Conflict Detection and Resolution

PostgreSQL logical replication has no built-in conflict handling. A duplicate key or constraint violation from a replicated row stops the apply worker, stalling replication.

### Check for conflicts

```bash
replicon conflicts -config replicon.json
```

This checks all nodes for:

- Stalled subscription workers (apply worker not running)
- Recent entries in the `replicon_conflict_log` table

### Resolution strategies

Configure in the `logical.conflict_resolution` section:

```json
{
  "logical": {
    "conflict_resolution": {
      "strategy": "skip"
    }
  }
}
```

| Strategy | Behavior |
|----------|----------|
| `log` (default) | Logs the conflict to `replicon_conflict_log`. Replication stalls until manually resolved. |
| `skip` | Logs the conflict and advances the subscription past the conflicting transaction. The conflicting row is lost on the subscriber side. This is a destructive operation. |
| `last_write_wins` | Not enforced by replicon — this is an application-level pattern. Set this strategy to document the intent and remind operators. The application must include `updated_at` columns and `ON CONFLICT` clauses. |

replicon records all detected conflicts in a `replicon_conflict_log` table on each node.

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

## Config Notes

- `dsn_env` is the recommended way to provide credentials. `dsn` works but puts connection strings in the config file.
- Rendered setup snippets use `REPL_PASSWORD` shell variable placeholders.
- `probe` writes to `public.replicon_replication_probe` — the DSN user needs CREATE and DML permissions.
- `promote` and `rejoin` use SSH for remote execution. Run `preflight` first to verify connectivity.
- Use `standbys` (array) for clusters with multiple standbys, or `standby` (object) for a single standby. Do not use both.
- Use `logical.nodes` (array) for multi-node logical replication, or `node_a`/`node_b` for two-node setups. The two-node fields are kept for backward compatibility.
- The `.pgpass` file on the old primary host is required for `rejoin` to authenticate `pg_basebackup` without prompting. See [deploy/pgpass.example](./deploy/pgpass.example).

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
make test              # unit tests
make test-race         # unit + stress tests with race detector
make test-integration  # integration tests against live PostgreSQL
make test-all          # lint + race + integration
make bench             # allocation benchmarks
make build             # build binary to bin/replicon
make docker-build      # build container image
make package-release   # cross-platform static binaries
```

CI: [ci.yml](./.github/workflows/ci.yml) | Release: [release.yml](./.github/workflows/release.yml)

## Scope and limitations

replicon manages PostgreSQL replication topologies with a single primary (master-slave/cluster) or multiple writable nodes (master-master).

**What works well:**

- Master-slave with one or more standbys: full lifecycle from setup through failover
- Automatic failover with fence-then-promote and optional witness node
- Active replication probing across all standbys in a cluster
- DDL capture and replay for master-master setups

**Where replicon has limits:**

- **No distributed consensus.** The witness node improves safety when SSH fails, but it is not equivalent to an etcd quorum. For the strongest automatic failover guarantees, use Patroni or Stolon.
- **Multi-node logical verify/probe not yet implemented.** `ddl-sync` and `conflicts` work across N nodes, but `verify` and `probe` only check the two-node `node_a`/`node_b` pair.
- **DDL sync is not real-time.** There is a window between DDL execution on one node and replay on others. Complex, data-dependent DDL may fail during replay.
- **Conflict resolution is limited.** `skip` advances past conflicts (lossy). `last_write_wins` is an application-level pattern that replicon documents but does not enforce. PostgreSQL does not provide a hook for custom conflict handlers in logical replication.
- **Master-master requires write partitioning.** replicon can detect and log conflicts but cannot prevent them at the database level.
