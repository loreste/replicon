# Integration Environment

This directory is a runnable local demo for `master-slave` replication.

## Start The Containers

```bash
docker compose -f integration/docker-compose.yml up -d
```

Stop and remove everything:

```bash
docker compose -f integration/docker-compose.yml down -v
```

## Intended Usage

- start a real primary and standby pair
- point `replicon` at `integration/replicon.master-slave.json`
- run `verify` and `probe` against the containers
- rehearse promote/rejoin in dry-run mode before touching real hosts

## Test The Demo

Start the stack:

```bash
export INTEGRATION_POSTGRES_PASSWORD=postgres
export INTEGRATION_REPLICATION_PASSWORD=postgres
export REPLICON_DEMO_PRIMARY_DSN='postgres://postgres:postgres@127.0.0.1:55432/postgres'
export REPLICON_DEMO_STANDBY_DSN='postgres://postgres:postgres@127.0.0.2:55433/postgres'
docker compose -f integration/docker-compose.yml up -d
```

Build the application image:

```bash
docker build -t replicon:local .
```

Verify replication:

```bash
docker run --rm --network host \
  -e REPLICON_DEMO_PRIMARY_DSN \
  -e REPLICON_DEMO_STANDBY_DSN \
  replicon:local verify -config /app/integration/replicon.master-slave.json
```

Run an active probe:

```bash
docker run --rm --network host \
  -e REPLICON_DEMO_PRIMARY_DSN \
  -e REPLICON_DEMO_STANDBY_DSN \
  replicon:local probe -config /app/integration/replicon.master-slave.json
```

Run the same kind of full database-object replication check we tested:

```bash
docker exec replicon-primary psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c 'CREATE DATABASE replicon_full_validation;'

docker exec replicon-primary psql -U postgres -d replicon_full_validation -v ON_ERROR_STOP=1 \
  -c "CREATE SCHEMA app;
      CREATE TABLE app.accounts (id integer PRIMARY KEY, email text NOT NULL UNIQUE);
      CREATE TABLE app.events (id bigserial PRIMARY KEY, account_id integer REFERENCES app.accounts(id));
      CREATE INDEX events_account_idx ON app.events(account_id);
      CREATE VIEW app.account_summary AS
        SELECT a.id, a.email, count(e.id) AS event_count
        FROM app.accounts a
        LEFT JOIN app.events e ON e.account_id = a.id
        GROUP BY a.id, a.email;
      INSERT INTO app.accounts VALUES (1, 'alice@example.test');
      INSERT INTO app.events (account_id) VALUES (1);"

docker exec replicon-standby psql -U postgres -d replicon_full_validation -v ON_ERROR_STOP=1 \
  -c "SELECT table_schema, table_name, table_type
      FROM information_schema.tables
      WHERE table_schema = 'app'
      ORDER BY table_name;
      SELECT schemaname, indexname, tablename
      FROM pg_indexes
      WHERE schemaname = 'app'
      ORDER BY indexname;
      SELECT id, email, event_count FROM app.account_summary ORDER BY id;"

docker exec replicon-primary psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c 'DROP DATABASE replicon_full_validation;'
```

## Multi-Version Testing

Test against PostgreSQL 13 and 14 using the additional compose files:

```bash
# PostgreSQL 13 (ports 55442/55443)
docker compose -f integration/docker-compose.pg13.yml -p replicon-pg13 up -d --build
REPLICON_DEMO_PRIMARY_DSN='postgres://postgres:postgres@127.0.0.1:55442/postgres' \
REPLICON_DEMO_STANDBY_DSN='postgres://postgres:postgres@127.0.0.1:55443/postgres' \
go run . verify -config integration/replicon.master-slave.json

# PostgreSQL 14 (ports 55452/55453)
docker compose -f integration/docker-compose.pg14.yml -p replicon-pg14 up -d --build
REPLICON_DEMO_PRIMARY_DSN='postgres://postgres:postgres@127.0.0.1:55452/postgres' \
REPLICON_DEMO_STANDBY_DSN='postgres://postgres:postgres@127.0.0.1:55453/postgres' \
go run . verify -config integration/replicon.master-slave.json
```

Tear down when done:

```bash
docker compose -f integration/docker-compose.pg13.yml -p replicon-pg13 down -v
docker compose -f integration/docker-compose.pg14.yml -p replicon-pg14 down -v
```

## Notes

- the demo uses password authentication with disposable local credentials
- use [demo.env.example](./demo.env.example) as the starting point for required environment variables
- `promote` and `rejoin` still assume SSH access for real execution, so use dry-run mode against this local demo
- this integration stack covers `master-slave`
- `master-master` has been tested with disposable PostgreSQL containers, but there is not yet a checked-in Compose stack for it
- the primary Dockerfile accepts a `PG_VERSION` build arg (defaults to 16)
