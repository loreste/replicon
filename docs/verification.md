# Verification And Probing

`replicon` has two runtime checks:

- `verify`: read-only inspection
- `probe`: active end-to-end replication test

## Verify

Run:

```bash
go run . verify -config <file>
```

What it does:

- `master-slave`: checks `pg_stat_replication` on the primary and replay state on the standby
- `master-master`: checks `pg_stat_subscription` on both nodes and treats subscriptions with active workers as streaming

Use `verify` when you want:

- a safe health check
- a fast operational check
- no writes to the database

## Probe

Run:

```bash
go run . probe -config <file>
```

What it does:

- creates `public.replicon_replication_probe` if it does not already exist
- writes a sentinel row
- waits for that row to replicate
- deletes the row
- waits for that delete to replicate

Use `probe` when you want:

- proof that data is really moving
- confirmation that insert and delete replication are both working
- a stronger check after setup or incident recovery

## Which One To Run

- Run `verify` for routine health checks.
- Run `probe` after initial setup, after maintenance, or when `verify` looks healthy but you want proof that replication is actually applying data.

For the Docker `master-slave` stack, we tested a temporary database on the primary with representative objects, then confirmed those objects were visible on the standby.

Example against the Docker integration stack:

```bash
docker exec replicon-primary psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c 'CREATE DATABASE replicon_full_validation;'

docker exec replicon-primary psql -U postgres -d replicon_full_validation -v ON_ERROR_STOP=1 \
  -c "CREATE SCHEMA app;
      CREATE SEQUENCE app.ticket_seq START 10;
      SELECT setval('app.ticket_seq', 42, true);
      CREATE TABLE app.accounts (
        id integer PRIMARY KEY,
        email text NOT NULL UNIQUE,
        balance numeric(12,2) NOT NULL DEFAULT 0
      );
      CREATE TABLE app.events (
        id bigserial PRIMARY KEY,
        account_id integer NOT NULL REFERENCES app.accounts(id),
        event_type text NOT NULL,
        payload jsonb NOT NULL DEFAULT '{}'::jsonb
      );
      CREATE INDEX events_account_type_idx ON app.events(account_id, event_type);
      CREATE VIEW app.account_summary AS
        SELECT a.id, a.email, count(e.id) AS event_count
        FROM app.accounts a
        LEFT JOIN app.events e ON e.account_id = a.id
        GROUP BY a.id, a.email;
      INSERT INTO app.accounts (id, email, balance)
        VALUES (1, 'alice@example.test', 10.50), (2, 'bob@example.test', 20.00);
      INSERT INTO app.events (account_id, event_type, payload)
        VALUES (1, 'created', '{\"source\":\"replicon\"}'::jsonb),
               (1, 'credited', '{\"amount\":10.50}'::jsonb),
               (2, 'created', '{\"source\":\"replicon\"}'::jsonb);"

docker exec replicon-standby psql -U postgres -d replicon_full_validation -v ON_ERROR_STOP=1 \
  -c "SELECT table_schema, table_name, table_type
      FROM information_schema.tables
      WHERE table_schema = 'app'
      ORDER BY table_name;
      SELECT schemaname, indexname, tablename
      FROM pg_indexes
      WHERE schemaname = 'app'
      ORDER BY indexname;
      SELECT 'accounts' AS table_name, count(*) FROM app.accounts
      UNION ALL
      SELECT 'events', count(*) FROM app.events
      ORDER BY table_name;
      SELECT id, email, event_count FROM app.account_summary ORDER BY id;
      SELECT last_value, is_called FROM app.ticket_seq;"

docker exec replicon-primary psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c 'DROP DATABASE replicon_full_validation;'
```

## Failure Interpretation

If `verify` fails:

- replication may be disconnected
- the standby may not be in recovery
- logical subscriptions may be down

If `probe` fails:

- catalog views may look healthy but data is not flowing
- permissions on the probe table may be missing
- publications or subscriptions may be incomplete
- replication lag may be high enough to hit the probe timeout
- for `master-master`, the table structure may be missing on one node or subscriptions may not have been refreshed after table creation
