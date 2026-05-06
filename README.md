# replicon

A small Go CLI that helps you prepare and verify PostgreSQL replication between two servers.

## Why this exists

PostgreSQL has solid built-in replication. Physical streaming replication and logical replication both work well once configured. The difficulty is everything around the setup and ongoing verification:

**Setting up replication is manual and error-prone.** Getting a primary-standby pair running requires editing `postgresql.conf`, `pg_hba.conf`, creating replication roles and slots, running `pg_basebackup` with the right flags, and configuring recovery parameters on the standby. Each step depends on the previous one being correct. A typo in a CIDR block or a missing `pg_hba.conf` entry means replication silently fails to connect. For logical replication (master-master), the surface area doubles: both nodes need matching configuration, publications, cross-subscriptions, and the `origin = none` flag to prevent infinite loops. There is no built-in tool that validates the whole chain before you start.

**There is no built-in way to confirm replication is actually working.** `pg_stat_replication` tells you a standby is connected, but not that data is flowing. A subscription can show `streaming` in `pg_stat_subscription` while the apply worker is silently stuck. The only way to be sure is to write data on one side and confirm it appears on the other. PostgreSQL does not provide a command for this.

**Failover is a series of manual steps with real consequences.** Promoting a standby is straightforward (`pg_promote()`), but the steps around it — fencing the old primary, rebuilding it as a standby, creating a new replication slot — are easy to get wrong under pressure. Getting the order wrong can cause split-brain or data loss.

**Configuration is scattered and not version-controlled.** Replication settings live across `postgresql.conf`, `pg_hba.conf`, replication slots, and recovery parameters. There is no single source of truth for what the intended topology looks like, which makes auditing and reproducing setups difficult.

### What already exists

There are good tools in this space, but they solve different problems:

- **Patroni** / **Stolon** / **repmgr** are full HA orchestrators that manage automatic failover, leader election, and cluster state via DCS (etcd, ZooKeeper, Consul). They are the right choice when you need automated failover for multi-node clusters. They are also complex to deploy and operate, and they take ownership of your PostgreSQL lifecycle.
- **pg_basebackup** and the PostgreSQL replication protocol are the underlying mechanisms. They work, but they are low-level building blocks, not end-to-end workflows.
- **pgBackRest** / **Barman** focus on backup and recovery, not replication topology management.

replicon does not compete with any of these. It is not an HA orchestrator. It does not manage leader election, does not run as a daemon watching your cluster, and does not take ownership of your PostgreSQL process. It is a configuration and verification tool for teams that:

- manage two-node replication setups (one primary and one standby, or two writable nodes with logical replication)
- want to define the intended topology in a single JSON file that can be version-controlled
- want to validate the configuration before touching the servers
- want a real end-to-end test that data is actually replicating, not just that connections are open
- want scripted failover steps that can be dry-run first and audited after execution
- do not need or want the operational overhead of a full HA framework

### What replicon actually does

It gives you a repeatable way to:

- keep the topology in one JSON file
- validate the replication plan before touching either server
- render the primary and standby configuration snippets you need
- generate the `pg_basebackup` bootstrap command for the standby
- verify that replication is actually active by querying PostgreSQL system views
- run an active write/read/delete probe across the configured replication path
- dry-run failover commands before executing them over SSH
- audit every operation in a JSONL log

### What replicon does not do

- Automatic failover beyond two nodes (two-node fence-then-promote is supported via `watch`)
- Cluster management beyond two nodes
- Continuous monitoring or alerting (though it exposes Prometheus metrics in service mode)
- DDL replication for master-master (a PostgreSQL limitation, not ours)
- Conflict resolution for master-master writes (the application must handle this)

## Tested Support

PostgreSQL versions tested:

| Version | Mode | Status |
|---------|------|--------|
| PostgreSQL 13 | master-slave | integration tests, verify, probe all pass |
| PostgreSQL 14 | master-slave | integration tests, verify, probe all pass |
| PostgreSQL 16 | master-slave | integration tests, verify, probe all pass |
| PostgreSQL 16 | master-slave (cluster) | verify, probe, best-standby promotion all pass |
| PostgreSQL 16 | master-master | verify, probe, bidirectional replication all pass |

replicon works the same way whether PostgreSQL is running in Docker, on bare-metal servers, or on cloud VMs. It connects over standard DSNs and uses SSH for remote operations — there is no Docker dependency at runtime.

The included Docker integration environment starts a real `master-slave` primary and standby. In that environment we tested streaming status, the active `probe`, and replication of a temporary database containing schemas, tables, rows, indexes, a sequence, a view, and a function from primary to standby. Multi-version compose files are provided for PG 13 and PG 14 testing.

For `master-master`, we tested `validate`, `plan`, `render -target node-a`, `render -target node-b`, `verify`, bidirectional row replication on a pre-created table, and the active two-way `probe` with disposable Docker containers. PostgreSQL logical replication does not automatically replicate databases or DDL; databases, schemas, and tables must exist on both nodes before table data can replicate.

`verify` is read-only. It checks PostgreSQL replication state views.

`probe` is active. It creates `public.replicon_replication_probe`, writes a sentinel row, waits for replication, deletes the row, and confirms the delete also replicates.

`serve` exposes a TLS-protected admin API with API-key authentication, audit logging, and Prometheus-style metrics.

`promote` and `rejoin` provide manual failover operations for `master-slave`, with dry-run output by default and SSH execution when `-execute` is set.

`watch` runs an automatic failover watchdog that monitors the primary and promotes the standby when the primary is unreachable. It uses a fence-then-promote model: it stops PostgreSQL on the old primary via SSH before promoting the standby, and refuses to promote if fencing fails (to prevent split-brain).

## Intended Architecture

Master/slave:

```mermaid
flowchart LR
    A[replicon CLI] --> B[Primary PostgreSQL]
    A --> C[Standby PostgreSQL]
    B -->|Physical streaming replication / WAL| C
    A -->|verify| D[pg_stat_replication]
    A -->|verify| E[Standby recovery and replay state]
    A -->|probe write| B
    A -->|probe read| C
```

Master/master:

```mermaid
flowchart LR
    A[replicon CLI] --> B[Node A PostgreSQL]
    A --> C[Node B PostgreSQL]
    B -->|Logical publication / subscription| C
    C -->|Logical publication / subscription| B
    A -->|verify| D[pg_stat_subscription on node A]
    A -->|verify| E[pg_stat_subscription on node B]
    A -->|probe write/read/delete| B
    A -->|probe write/read/delete| C
```

## Modes

replicon supports three topologies:

- **master-slave** — one writable primary, one read-only standby. The simplest setup.
- **master-slave (cluster)** — one writable primary, multiple read-only standbys. Use the `standbys` array instead of the single `standby` field. Promotion picks the standby with the least replication lag.
- **master-master** — two writable nodes with logical bidirectional replication. Requires the application to prevent conflicting writes.

## Quick Start

Master/slave:

```bash
export REPLICON_PRIMARY_DSN='postgres://postgres:secret@10.0.0.10:5432/postgres?sslmode=require'
export REPLICON_STANDBY_DSN='postgres://postgres:secret@10.0.0.11:5432/postgres?sslmode=require'
go run . init -mode master-slave > replicon.json
go run . validate -config replicon.json
go run . plan -config replicon.json
go run . render -config replicon.json -target primary
go run . render -config replicon.json -target standby
go run . verify -config replicon.json
go run . probe -config replicon.json
go run . promote -config replicon.json
go run . rejoin -config replicon.json
```

Master/master:

```bash
export REPLICON_NODE_A_DSN='postgres://postgres:secret@10.0.0.10:5432/appdb?sslmode=require'
export REPLICON_NODE_B_DSN='postgres://postgres:secret@10.0.0.11:5432/appdb?sslmode=require'
go run . init -mode master-master > replicon-mm.json
go run . validate -config replicon-mm.json
go run . render -config replicon-mm.json -target node-a
go run . render -config replicon-mm.json -target node-b
go run . verify -config replicon-mm.json
go run . probe -config replicon-mm.json
```

Cluster (multiple standbys):

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

`verify` and `probe` check all standbys. `promote` queries each standby's WAL receive position and promotes the one closest to the primary. Use `standbys` (array) instead of `standby` (single object) — do not use both.

## Commands

- `go run . init -mode master-slave`
- `go run . init -mode master-master`
- `go run . validate -config <file>`
- `go run . plan -config <file>`
- `go run . render -config <file> -target primary`
- `go run . render -config <file> -target standby`
- `go run . render -config <file> -target node-a`
- `go run . render -config <file> -target node-b`
- `go run . verify -config <file> [-output text|json] [-audit-log path]`
- `go run . probe -config <file> [-output text|json] [-audit-log path]`
- `go run . promote -config <file> [-execute] [-output text|json] [-audit-log path]`
- `go run . rejoin -config <file> [-execute] [-output text|json] [-audit-log path]`
- `go run . watch -config <file> [-audit-log path]`
- `go run . history [-audit-log path] [-limit 20] [-output text|json]`
- `go run . serve -config <file> -tls-cert server.crt -tls-key server.key`

## Config Notes

- `mode` chooses the replication model.
- `dsn_env` is the preferred production path for node credentials.
- `dsn` still works, but it keeps database connection material in the config file.
- Rendered setup snippets use `REPL_PASSWORD` shell variable placeholders — secrets should be stored outside of the config file (e.g. in environment variables or a secrets manager).
- `probe` writes into `public.replicon_replication_probe`, so the configured DSN user must be able to create and modify that table.
- `serve` requires `REPLICON_API_KEY` by default, plus TLS certificate and key files.
- `promote` and `rejoin` use the local `ssh` binary for remote execution when `-execute` is set.

## Failover Operations

Plan the workflow without touching the servers:

```bash
go run . promote -config replicon.json
go run . rejoin -config replicon.json
```

Execute over SSH:

```bash
go run . promote -config replicon.json -execute
go run . rejoin -config replicon.json -execute
```

`promote` makes the configured standby writable.

`rejoin` preserves the old primary data directory under a timestamped backup path, rebuilds it from the new primary, and starts it back as a standby.

## Automatic Failover

Add a `failover` section to your config:

```json
{
  "cluster_name": "orders-prod",
  "mode": "master-slave",
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
go run . watch -config replicon.json -audit-log var/audit/replicon.jsonl
```

How it works:

1. Checks primary health every `check_interval_sec` seconds via a SQL connection.
2. After `max_failures` consecutive failures, fences the primary via SSH (`fence_command`).
3. If fencing succeeds, promotes the standby.
4. If fencing fails (e.g. network partition), does **not** promote — to prevent split-brain.
5. Optionally runs `post_promote_command` on the new primary (e.g. update DNS).

The watchdog runs preflight and verify checks before entering the monitoring loop. All fence and promote events are recorded in the audit log.

| Field | Default | Description |
|-------|---------|-------------|
| `check_interval_sec` | 5 | Seconds between health checks |
| `health_timeout_sec` | 3 | Timeout for each health check connection |
| `max_failures` | 3 | Consecutive failures before triggering failover |
| `fence_timeout_sec` | 10 | Timeout for the SSH fence command |
| `fence_command` | `sudo systemctl stop postgresql` | Command to stop PostgreSQL on the primary |
| `post_promote_command` | (none) | Optional command to run on the new primary after promotion |

## Audit History

Read recent audit entries from the JSONL audit log:

```bash
go run . history -audit-log var/audit/replicon.jsonl -limit 20
go run . history -audit-log var/audit/replicon.jsonl -output json
```

## Service Mode

Start the admin API:

```bash
export REPLICON_API_KEY='replace-with-long-random-token'
go run . serve \
  -config replicon.json \
  -listen :8443 \
  -tls-cert server.crt \
  -tls-key server.key \
  -audit-log var/audit/replicon.jsonl
```

Example API calls:

```bash
curl -s \
  -H 'X-API-Key: replace-with-long-random-token' \
  https://127.0.0.1:8443/healthz

curl -s \
  -H 'X-API-Key: replace-with-long-random-token' \
  https://127.0.0.1:8443/readyz

curl -s \
  -H 'X-API-Key: replace-with-long-random-token' \
  https://127.0.0.1:8443/api/v1/verify

curl -s \
  -H 'X-API-Key: replace-with-long-random-token' \
  'https://127.0.0.1:8443/api/v1/history?limit=10'

curl -s -X POST \
  -H 'X-API-Key: replace-with-long-random-token' \
  https://127.0.0.1:8443/api/v1/promote

curl -s -X POST \
  -H 'X-API-Key: replace-with-long-random-token' \
  https://127.0.0.1:8443/api/v1/rejoin

curl -s \
  -H 'X-API-Key: replace-with-long-random-token' \
  https://127.0.0.1:8443/metrics
```

## How-To Docs

- [Linux Installation And Configuration](./docs/linux-setup.md) — complete step-by-step guide for bare-metal and VM servers
- [Installation](./docs/installation.md)
- [Master-Slave Setup](./docs/master-slave.md)
- [Master-Master Setup](./docs/master-master.md)
- [Verification And Probing](./docs/verification.md)
- [Service Mode](./docs/service-mode.md)
- [Deployment](./docs/deployment.md)
- [Integration Environment](./integration/README.md)

## Automation

Local automation:

```bash
make test
make build
make docker-build
make package-release
```

CI:

- GitHub Actions workflow: [ci.yml](./.github/workflows/ci.yml)
- GitHub Actions release workflow: [release.yml](./.github/workflows/release.yml)

## Release Assets

- sample configs: [config/master-slave.example.json](./config/master-slave.example.json), [config/master-master.example.json](./config/master-master.example.json)
- local cross-platform packaging: [scripts/package-release.sh](./scripts/package-release.sh)

## Example

```bash
go run . init > replicon.json
go run . validate -config replicon.json
go run . render -config replicon.json -target primary
go run . render -config replicon.json -target standby
go run . verify -config replicon.json -output json
go run . probe -config replicon.json -audit-log var/audit/replicon.jsonl
go run . promote -config replicon.json
go run . history -audit-log var/audit/replicon.jsonl
```

## Scope and limitations

replicon handles one primary with one or more standbys (master-slave), or two writable nodes with bidirectional logical replication (master-master). Cluster mode (multiple standbys) is supported by using the `standbys` array in the config. Automatic failover is available via `watch` for master-slave setups. It does not manage leader election via distributed consensus or orchestrate clusters beyond a single primary. If you need multi-primary or consensus-based HA, use Patroni or a similar orchestrator.

For `master-master`: PostgreSQL logical replication does not make conflicting writes safe. You still need a write ownership strategy in the application (region-based, tenant-based, or dataset partitioning). Logical replication also does not replicate DDL — schemas and tables must exist on both nodes before data can flow.

Docker Compose stacks are provided for both `master-slave` (including PG 13, 14, and 16) and `master-master`.
