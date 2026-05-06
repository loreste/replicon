# Installation

This document covers how to install `replicon` for local use, container use, and long-running service use.

## Requirements

- Go 1.26.1 or newer if you are building from source
- Docker if you want to use the container image or local integration demo
- PostgreSQL 13 or newer (tested on 13, 14, and 16)
- SSH access to target servers for `promote` and `rejoin` operations

replicon connects to PostgreSQL over standard DSNs and uses SSH for remote operations. It works the same way whether PostgreSQL is running in Docker, on bare-metal servers, or on cloud VMs — there is no Docker dependency at runtime.

## Option 1: Run From Source

From the repo root:

```bash
go run . --help
```

This is the fastest path for development or local testing.

## Option 2: Build A Local Binary

Build the binary:

```bash
make build
```

The binary will be written to:

```bash
bin/replicon
```

Run it:

```bash
./bin/replicon --help
```

## Option 3: Install With Go

If you want a binary in your Go bin path:

```bash
go install .
```

Then run:

```bash
replicon --help
```

## Option 4: Build A Container Image

Build:

```bash
docker build -t replicon:local .
```

Run:

```bash
docker run --rm replicon:local --help
```

For full API mode usage, see [Deployment](./deployment.md).

## Option 5: Install On A Bare-Metal Or VM Server

Build a static binary and copy it to the target machine:

```bash
make build
scp bin/replicon admin@db-server:/usr/local/bin/replicon
```

Then on the server, create a config and set environment variables for the DSNs:

```bash
ssh admin@db-server
replicon init -mode master-slave > /etc/replicon/replicon.json
# edit the config to match your cluster
export REPLICON_PRIMARY_DSN='postgres://postgres:secret@primary-host:5432/postgres?sslmode=require'
export REPLICON_STANDBY_DSN='postgres://postgres:secret@standby-host:5432/postgres?sslmode=require'
replicon validate -config /etc/replicon/replicon.json
replicon verify -config /etc/replicon/replicon.json
```

For cross-platform builds (e.g. building on macOS for a Linux server):

```bash
make package-release
# produces static binaries under dist/ for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
scp dist/replicon-linux-amd64 admin@db-server:/usr/local/bin/replicon
```

## Option 6: Install As A systemd Service

Files provided:

- [deploy/replicon.service](../deploy/replicon.service)
- [deploy/replicon.env.example](../deploy/replicon.env.example)

Typical install flow:

1. Build the binary with `make build` (or use a cross-compiled binary from `make package-release`)
2. Copy `bin/replicon` to `/usr/local/bin/replicon`
3. Copy your config to `/etc/replicon/replicon.json`
4. Copy TLS files to `/etc/replicon/tls/`
5. Copy your env file to `/etc/replicon/replicon.env`
6. Copy `deploy/replicon.service` to `/etc/systemd/system/replicon.service`
7. Create the audit directory and service user:

```bash
sudo useradd --system --shell /usr/sbin/nologin replicon
sudo mkdir -p /var/lib/replicon/audit
sudo chown replicon:replicon /var/lib/replicon/audit
```

8. Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now replicon
```

## Verify The Install

CLI:

```bash
replicon --help
```

Local build:

```bash
make test
```

Local demo (Docker):

```bash
docker compose -f integration/docker-compose.yml up -d --build
go run . verify -config integration/replicon.master-slave.json
go run . probe -config integration/replicon.master-slave.json
```

Multi-version testing (PostgreSQL 13, 14):

```bash
docker compose -f integration/docker-compose.pg13.yml -p replicon-pg13 up -d --build
docker compose -f integration/docker-compose.pg14.yml -p replicon-pg14 up -d --build
```

Bare-metal / VM verify:

```bash
replicon verify -config /etc/replicon/replicon.json
replicon probe -config /etc/replicon/replicon.json
```

## PostgreSQL Server Prerequisites

Before running replicon against real (non-Docker) PostgreSQL servers, ensure:

1. **PostgreSQL 13 or newer** is installed on both primary and standby.

2. **Primary postgresql.conf** has replication enabled:

   ```
   wal_level = replica              # or 'logical' for master-master
   max_wal_senders = 10
   max_replication_slots = 10
   hot_standby = on
   ```

3. **Primary pg_hba.conf** allows the replication user from the standby:

   ```
   host replication replicator <standby-cidr> scram-sha-256
   ```

4. **Replication role** exists on the primary:

   ```sql
   CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'your-password';
   ```

5. **Replication slot** exists on the primary:

   ```sql
   SELECT pg_create_physical_replication_slot('your_slot_name');
   ```

6. **Standby** was bootstrapped with `pg_basebackup`:

   ```bash
   pg_basebackup \
     --pgdata=/var/lib/postgresql/16/main \
     --write-recovery-conf \
     --slot=your_slot_name \
     --host=primary-host \
     --port=5432 \
     --username=replicator \
     --checkpoint=fast \
     --progress
   ```

7. **SSH access** is configured between the replicon host and PostgreSQL servers (needed only for `promote` and `rejoin` with `-execute`). Key-based authentication is recommended:

   ```bash
   ssh-copy-id ubuntu@primary-host
   ssh-copy-id ubuntu@standby-host
   replicon preflight -config /etc/replicon/replicon.json
   ```

8. **DSN environment variables** are set on the host where replicon runs:

   ```bash
   export REPLICON_PRIMARY_DSN='postgres://postgres:secret@primary-host:5432/postgres?sslmode=require'
   export REPLICON_STANDBY_DSN='postgres://postgres:secret@standby-host:5432/postgres?sslmode=require'
   ```

For master-master logical replication prerequisites, see [Master-Master Setup](./master-master.md).

## Example: Full Non-Docker Workflow

```bash
# 1. Generate and edit config
replicon init -mode master-slave > /etc/replicon/replicon.json

# 2. Validate the config
replicon validate -config /etc/replicon/replicon.json

# 3. Review the plan and rendered configuration snippets
replicon plan -config /etc/replicon/replicon.json
replicon render -config /etc/replicon/replicon.json -target primary
replicon render -config /etc/replicon/replicon.json -target standby

# 4. Apply the rendered snippets to your PostgreSQL servers (manual step)

# 5. Verify replication is streaming
replicon verify -config /etc/replicon/replicon.json

# 6. Run an active probe to confirm end-to-end replication
replicon probe -config /etc/replicon/replicon.json

# 7. (Optional) Check SSH connectivity before failover operations
replicon preflight -config /etc/replicon/replicon.json

# 8. (Optional) Dry-run a failover
replicon promote -config /etc/replicon/replicon.json

# 9. (Optional) Execute a failover
replicon promote -config /etc/replicon/replicon.json -execute
```

## Related Docs

- [Linux Installation And Configuration](./linux-setup.md) — complete step-by-step guide for bare-metal and VM servers
- [Master-Slave Setup](./master-slave.md)
- [Master-Master Setup](./master-master.md)
- [Service Mode](./service-mode.md)
- [Deployment](./deployment.md)
- [Integration Environment](../integration/README.md)
