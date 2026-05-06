# Master-Slave Setup

This mode builds native PostgreSQL physical streaming replication between one primary and one standby.

## When To Use It

Use `master-slave` when you want:

- one writable node
- one replica for failover or read-only access
- the simplest operational model for two servers

## 1. Create The Config

```bash
go run . init -mode master-slave > replicon.json
```

Edit `replicon.json` and set:

- primary host, port, and DSN
- standby host, port, and DSN
- replication slot name
- replication CIDR
- application name

## 2. Validate The Config

```bash
go run . validate -config replicon.json
```

This checks that the topology is complete and internally consistent before you touch either server.

## 3. Generate The Replication Plan

```bash
go run . plan -config replicon.json
go run . render -config replicon.json -target primary
go run . render -config replicon.json -target standby
```

Use the rendered output to:

- update `postgresql.conf`
- update `pg_hba.conf`
- create the replication role
- create the physical replication slot
- run `pg_basebackup` on the standby

## 4. Verify Replication

```bash
go run . verify -config replicon.json
```

This checks:

- `pg_stat_replication` on the primary
- `pg_is_in_recovery()` on the standby
- WAL receive and replay positions on the standby

## 5. Run An Active Probe

```bash
go run . probe -config replicon.json
```

This is stronger than `verify`. It:

1. creates `public.replicon_replication_probe` on the primary if needed
2. inserts a probe row on the primary
3. waits for that row to appear on the standby
4. deletes the probe row on the primary
5. waits for the delete to appear on the standby

## Docker Integration Validation

The repository includes a runnable `master-slave` Docker environment. We tested it with a PostgreSQL primary and standby, `verify`, `probe`, and representative database-object replication.

```bash
export INTEGRATION_POSTGRES_PASSWORD=postgres
export INTEGRATION_REPLICATION_PASSWORD=postgres
export REPLICON_DEMO_PRIMARY_DSN='postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable'
export REPLICON_DEMO_STANDBY_DSN='postgres://postgres:postgres@127.0.0.2:55433/postgres?sslmode=disable'

docker compose -f integration/docker-compose.yml up -d --build
docker build -t replicon:local .

docker run --rm --network host \
  -e REPLICON_DEMO_PRIMARY_DSN \
  -e REPLICON_DEMO_STANDBY_DSN \
  replicon:local verify -config /app/integration/replicon.master-slave.json

docker run --rm --network host \
  -e REPLICON_DEMO_PRIMARY_DSN \
  -e REPLICON_DEMO_STANDBY_DSN \
  replicon:local probe -config /app/integration/replicon.master-slave.json
```

In the Docker stack, we tested a temporary database with schemas, tables, rows, indexes, a sequence, a view, and a function. Those objects were readable on the standby after creation on the primary.

## Operational Notes

- The DSN user must be able to connect and query PostgreSQL for `verify`.
- The DSN user must also be able to create, insert into, and delete from `public.replicon_replication_probe` for `probe`.
- `probe` is safe for repeated use, but it does perform real writes.
