# Master-Master Setup

This mode is intended for PostgreSQL logical bidirectional replication between two writable nodes.

Logical replication works at the row level within a single database. Unlike physical replication (master-slave), it does **not** replicate the entire PostgreSQL instance. You must:

- Create the target database on both nodes manually
- Create matching schemas and tables on both nodes manually (DDL is not replicated)
- Explicitly publish the tables you want replicated
- Manage new table additions by refreshing subscriptions

Current test status: we tested `validate`, `plan`, both render targets, `verify`, direct bidirectional row replication, and `probe` with two disposable PostgreSQL 16 Docker containers.

## When To Use It

Use `master-master` only when your application can prevent conflicting writes.

Good examples:

- region ownership
- tenant ownership
- key-range ownership
- one side writes one dataset, the other side writes a different dataset

Bad example:

- both nodes freely updating the same rows

## 1. Create The Config

```bash
go run . init -mode master-master > replicon-mm.json
```

Edit `replicon-mm.json` and set:

- node A and node B host, port, and DSN
- logical database name
- publication names
- subscription names
- logical replication CIDR

## 2. Validate The Config

```bash
go run . validate -config replicon-mm.json
```

This checks that both nodes and logical replication settings are present.

## 3. Generate The Setup Output

```bash
go run . plan -config replicon-mm.json
go run . render -config replicon-mm.json -target node-a
go run . render -config replicon-mm.json -target node-b
```

Use the rendered output to:

- enable `wal_level = logical`
- configure replication access in `pg_hba.conf`
- create the replication role
- create publications
- create cross-subscriptions

## 4. Verify Replication

```bash
go run . verify -config replicon-mm.json
```

When run against a live `master-master` environment, this checks `pg_stat_subscription` on both nodes and expects both subscriptions to be streaming.

## 5. Run An Active Probe

```bash
go run . probe -config replicon-mm.json
```

When run against a live `master-master` environment, this validates both directions:

1. write on node A and confirm it appears on node B
2. delete on node A and confirm the delete reaches node B
3. write on node B and confirm it appears on node A
4. delete on node B and confirm the delete reaches node A

## Docker Validation Status

The tested Docker path for `master-master` covers config validation, planning, rendering, live subscription verification, direct row replication in both directions, and active probing.

Build the image and validate the example config:

```bash
docker build -t replicon:local .

docker run --rm \
  -v "$PWD/config:/config:ro" \
  replicon:local validate -config /config/master-master.example.json

docker run --rm \
  -v "$PWD/config:/config:ro" \
  replicon:local plan -config /config/master-master.example.json

docker run --rm \
  -v "$PWD/config:/config:ro" \
  replicon:local render -config /config/master-master.example.json -target node-a

docker run --rm \
  -v "$PWD/config:/config:ro" \
  replicon:local render -config /config/master-master.example.json -target node-b
```

For the live test, use two PostgreSQL nodes with:

- `wal_level = logical`
- matching database/schema/table definitions on both nodes
- publications on both nodes
- cross-subscriptions using `origin = none`

Then run:

```bash
docker run --rm --network host \
  -v "$PWD/config:/config:ro" \
  -e REPLICON_NODE_A_DSN='postgres://postgres:postgres@127.0.0.1:55532/appdb?sslmode=disable' \
  -e REPLICON_NODE_B_DSN='postgres://postgres:postgres@127.0.0.1:55533/appdb?sslmode=disable' \
  replicon:local verify -config /config/master-master.example.json

docker run --rm --network host \
  -v "$PWD/config:/config:ro" \
  -e REPLICON_NODE_A_DSN='postgres://postgres:postgres@127.0.0.1:55532/appdb?sslmode=disable' \
  -e REPLICON_NODE_B_DSN='postgres://postgres:postgres@127.0.0.1:55533/appdb?sslmode=disable' \
  replicon:local probe -config /config/master-master.example.json
```

## DDL Tracking

PostgreSQL logical replication does not replicate DDL. replicon can capture DDL via event triggers and replay it to other nodes:

```bash
replicon ddl-setup -config replicon-mm.json    # install event triggers on all nodes
replicon ddl-sync -config replicon-mm.json     # replay pending DDL to other nodes
```

This is not real-time — run `ddl-sync` after making schema changes, or schedule it on a cron. See the [README](../README.md#ddl-tracking-and-sync) for details and caveats.

## Conflict Detection

Check for stalled subscriptions or logged conflicts:

```bash
replicon conflicts -config replicon-mm.json
```

Configure a resolution strategy in the config:

```json
{
  "logical": {
    "conflict_resolution": {
      "strategy": "skip"
    }
  }
}
```

Available strategies: `log` (default — stalls until manual resolution), `skip` (advances past the conflict, lossy), `last_write_wins` (application-level pattern, not enforced by replicon).

## Multi-Node Logical Replication

For more than two writable nodes, use `logical.nodes` instead of `node_a`/`node_b`:

```json
{
  "logical": {
    "database": "appdb",
    "nodes": [
      { "name": "us-east", "host": "10.0.0.10", ... },
      { "name": "us-west", "host": "10.0.0.11", ... },
      { "name": "eu-central", "host": "10.0.0.12", ... }
    ]
  }
}
```

`ddl-sync` and `conflicts` operate across all nodes. `verify` and `probe` currently only support the two-node `node_a`/`node_b` configuration.

## Operational Notes

- Logical replication does not make concurrent writes conflict-safe. The application must partition writes.
- Logical replication does not replicate databases, schemas, or table DDL. Use `ddl-setup` and `ddl-sync` or create matching structures manually.
- Bidirectional subscriptions must avoid republishing replicated changes. The rendered setup uses `origin = none` for this.
- The DSN user must be able to create and modify `public.replicon_replication_probe`.
- `probe` refreshes both subscriptions after creating the probe table so the table is included in the publications.
