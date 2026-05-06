# Linux Installation And Configuration

This is a complete step-by-step guide for setting up replicon and PostgreSQL replication on Linux servers. It covers both master-slave (physical streaming) and master-master (logical bidirectional) topologies.

No Docker is required. This guide assumes you are working with bare-metal servers or VMs running a Debian/Ubuntu or RHEL/Rocky-based distribution.

## Prerequisites

| Requirement | Notes |
|-------------|-------|
| Two Linux servers | Referred to as **server-a** and **server-b** throughout this guide |
| PostgreSQL 13, 14, 15, or 16 | Same major version on both servers |
| Network connectivity | Both servers must reach each other on the PostgreSQL port (default 5432) |
| SSH access | From the machine running replicon to both servers (needed for promote/rejoin) |
| Root or sudo | For installing packages, editing PostgreSQL config, and managing services |

## Part 1: Install PostgreSQL On Both Servers

### Debian / Ubuntu

Run these steps on **both** server-a and server-b.

```bash
# Add the official PostgreSQL repository
sudo apt-get install -y curl ca-certificates gnupg
curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | sudo gpg --dearmor -o /usr/share/keyrings/postgresql-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/postgresql-keyring.gpg] http://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" | sudo tee /etc/apt/sources.list.d/pgdg.list

# Install PostgreSQL (replace 16 with your version)
sudo apt-get update
sudo apt-get install -y postgresql-16

# Confirm it is running
sudo systemctl status postgresql
sudo -u postgres psql -c "SELECT version();"
```

### RHEL / Rocky / AlmaLinux

Run these steps on **both** server-a and server-b.

```bash
# Install the repository RPM (replace 16 with your version)
sudo dnf install -y https://download.postgresql.org/pub/repos/yum/reporpms/EL-$(rpm -E %{rhel})-x86_64/pgdg-redhat-repo-latest.noarch.rpm

# Disable the built-in module if present
sudo dnf -qy module disable postgresql 2>/dev/null || true

# Install PostgreSQL
sudo dnf install -y postgresql16-server postgresql16

# Initialize the database
sudo /usr/pgsql-16/bin/postgresql-16-setup initdb

# Start and enable
sudo systemctl enable --now postgresql-16

# Confirm it is running
sudo systemctl status postgresql-16
sudo -u postgres psql -c "SELECT version();"
```

> **Note:** On RHEL-based systems the data directory is typically `/var/lib/pgsql/16/data` and the service name is `postgresql-16`. On Debian-based systems the data directory is `/var/lib/postgresql/16/main` and the service name is `postgresql`. Adjust paths in the replicon config accordingly.

---

## Part 2: Install replicon

replicon is a single static binary. Install it on the machine that will manage the cluster. This can be one of the PostgreSQL servers or a separate management host.

### Option A: Download a pre-built binary

If a release binary is available:

```bash
curl -fsSL https://github.com/your-org/replicon/releases/latest/download/replicon-linux-amd64 -o /usr/local/bin/replicon
chmod +x /usr/local/bin/replicon
replicon help
```

### Option B: Build from source

Requires Go 1.26.1 or newer.

```bash
git clone https://github.com/your-org/replicon.git
cd replicon
make build
sudo cp bin/replicon /usr/local/bin/replicon
replicon help
```

### Option C: Cross-compile on another machine

Build on your laptop, copy to the server:

```bash
# On your build machine
cd replicon
make package-release
scp dist/replicon-linux-amd64 admin@server-a:/usr/local/bin/replicon
ssh admin@server-a 'chmod +x /usr/local/bin/replicon'
```

---

## Part 3: Master-Slave Setup

This sets up physical streaming replication: one writable primary (server-a) and one read-only standby (server-b).

### Step 1: Configure the primary (server-a)

Edit the PostgreSQL configuration. On Debian the file is at `/etc/postgresql/16/main/postgresql.conf`. On RHEL it is at `/var/lib/pgsql/16/data/postgresql.conf`.

```bash
# On server-a
sudo -u postgres vi /etc/postgresql/16/main/postgresql.conf
```

Set or verify these parameters:

```
listen_addresses = '*'
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
```

### Step 2: Configure host-based authentication on the primary

Edit `pg_hba.conf` (same directory as `postgresql.conf`):

```bash
sudo -u postgres vi /etc/postgresql/16/main/pg_hba.conf
```

Add a line that allows the standby to connect for replication. Replace `<standby-ip>` with server-b's IP address:

```
host    replication     replicator      <standby-ip>/32     scram-sha-256
```

For example:

```
host    replication     replicator      10.0.0.11/32        scram-sha-256
```

Reload PostgreSQL to pick up the changes:

```bash
sudo systemctl reload postgresql
```

### Step 3: Create the replication user and slot on the primary

```bash
# On server-a
sudo -u postgres psql <<'SQL'
CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'your-secure-password';
SELECT pg_create_physical_replication_slot('your_slot_name');
SQL
```

Choose a descriptive slot name like `orders_prod_standby`.

### Step 4: Bootstrap the standby (server-b)

Stop PostgreSQL on the standby, wipe its data directory, and replicate from the primary:

```bash
# On server-b
sudo systemctl stop postgresql

# Back up the existing data directory just in case
sudo -u postgres mv /var/lib/postgresql/16/main /var/lib/postgresql/16/main.bak

# Run pg_basebackup from the primary
sudo -u postgres PGPASSWORD='your-secure-password' pg_basebackup \
  --pgdata=/var/lib/postgresql/16/main \
  --write-recovery-conf \
  --slot=your_slot_name \
  --host=<primary-ip> \
  --port=5432 \
  --username=replicator \
  --checkpoint=fast \
  --progress
```

Verify that `postgresql.auto.conf` on the standby now contains the `primary_conninfo` and `primary_slot_name` settings:

```bash
sudo -u postgres cat /var/lib/postgresql/16/main/postgresql.auto.conf
```

Start PostgreSQL on the standby:

```bash
sudo systemctl start postgresql
```

Verify the standby is in recovery mode:

```bash
sudo -u postgres psql -tAc "SELECT pg_is_in_recovery();"
# Expected: t
```

### Step 5: Verify replication from the primary

On server-a, check that the standby is connected:

```bash
sudo -u postgres psql -c "SELECT application_name, client_addr, state, sync_state FROM pg_stat_replication;"
```

You should see the standby listed with `state = streaming`.

### Step 6: Create the replicon configuration

On the machine where replicon is installed:

```bash
replicon init -mode master-slave > /etc/replicon/replicon.json
```

Edit `/etc/replicon/replicon.json` and fill in your cluster details:

```json
{
  "cluster_name": "orders-prod",
  "mode": "master-slave",
  "replication_user": "replicator",
  "replication_slot": "your_slot_name",
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
  "standby": {
    "name": "standby",
    "host": "10.0.0.11",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-b",
    "dsn_env": "REPLICON_STANDBY_DSN"
  },
  "network": {
    "replication_cidr": "10.0.0.11/32",
    "application_name": "orders-prod-standby"
  }
}
```

Key fields to set:

| Field | What to put |
|-------|-------------|
| `host` | The IP or hostname of each server |
| `port` | PostgreSQL port (usually 5432) |
| `data_dir` | The PostgreSQL data directory on that server |
| `postgres_user` | The OS user that owns the PostgreSQL process (usually `postgres`) |
| `ssh_user` | The OS user replicon will SSH as for promote/rejoin |
| `server_id` | A unique identifier for the node |
| `dsn_env` | The environment variable name that holds the connection string |
| `replication_cidr` | The standby's IP in CIDR notation |

### Step 7: Set DSN environment variables

```bash
export REPLICON_PRIMARY_DSN='postgres://postgres:dbpassword@10.0.0.10:5432/postgres?sslmode=require'
export REPLICON_STANDBY_DSN='postgres://postgres:dbpassword@10.0.0.11:5432/postgres?sslmode=require'
```

For persistence, add these to `/etc/replicon/replicon.env`:

```bash
REPLICON_PRIMARY_DSN=postgres://postgres:dbpassword@10.0.0.10:5432/postgres?sslmode=require
REPLICON_STANDBY_DSN=postgres://postgres:dbpassword@10.0.0.11:5432/postgres?sslmode=require
```

### Step 8: Validate, verify, and probe

```bash
# Validate the config structure
replicon validate -config /etc/replicon/replicon.json

# Read-only check of replication state
replicon verify -config /etc/replicon/replicon.json

# Active write/read/delete replication test
replicon probe -config /etc/replicon/replicon.json
```

Expected output for verify:

```
Replication verification: PASS
Mode: master-slave
Primary: primary
Standby: standby
Primary view: application_name=orders-prod-standby client_addr=10.0.0.11/32 state=streaming sync_state=async
Standby view: in_recovery=true receive_lsn=0/3000148 replay_lsn=0/3000148 replay_delay_seconds=0
```

Expected output for probe:

```
Active replication probe: PASS
Mode: master-slave
Probe table: public.replicon_replication_probe
Wrote on: primary
Observed on: standby
Deletion replay confirmed on standby
```

---

## Part 4: Master-Master Setup

This sets up logical bidirectional replication: both server-a and server-b are writable. Requires PostgreSQL 13 or newer.

> **Warning:** Logical replication does not prevent write conflicts. Your application must ensure that both nodes do not update the same rows concurrently. Common strategies: region-based ownership, tenant-based ownership, or dataset partitioning.

### Step 1: Configure both nodes

On **both** server-a and server-b, edit `postgresql.conf`:

```
listen_addresses = '*'
wal_level = logical
max_wal_senders = 10
max_replication_slots = 10
```

Edit `pg_hba.conf` on **both** nodes to allow replication from the other node:

```
host    replication     replicator      10.0.0.0/24         scram-sha-256
host    appdb           replicator      10.0.0.0/24         scram-sha-256
```

Reload both:

```bash
sudo systemctl reload postgresql
```

### Step 2: Create the replication user on both nodes

Run on **both** server-a and server-b:

```bash
sudo -u postgres psql <<'SQL'
CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'your-secure-password';
SQL
```

### Step 3: Create the database and tables on both nodes

Logical replication does not replicate DDL. The database, schemas, and tables must exist on both nodes before data can replicate.

Run on **both** server-a and server-b:

```bash
sudo -u postgres psql <<'SQL'
CREATE DATABASE appdb;
SQL

sudo -u postgres psql -d appdb <<'SQL'
CREATE TABLE orders (
    id bigserial PRIMARY KEY,
    region text NOT NULL,
    customer_id integer NOT NULL,
    total numeric(12,2) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Grant the replication user access
GRANT ALL ON ALL TABLES IN SCHEMA public TO replicator;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO replicator;
SQL
```

### Step 4: Create publications on both nodes

On **server-a**:

```bash
sudo -u postgres psql -d appdb -c "CREATE PUBLICATION orders_pub_a FOR ALL TABLES;"
```

On **server-b**:

```bash
sudo -u postgres psql -d appdb -c "CREATE PUBLICATION orders_pub_b FOR ALL TABLES;"
```

### Step 5: Create cross-subscriptions

On **server-a** (subscribing to server-b's publication):

```bash
sudo -u postgres psql -d appdb <<SQL
CREATE SUBSCRIPTION orders_sub_a
CONNECTION 'host=10.0.0.11 port=5432 dbname=appdb user=replicator password=your-secure-password'
PUBLICATION orders_pub_b
WITH (copy_data = false, create_slot = true, enabled = true, origin = none);
SQL
```

On **server-b** (subscribing to server-a's publication):

```bash
sudo -u postgres psql -d appdb <<SQL
CREATE SUBSCRIPTION orders_sub_b
CONNECTION 'host=10.0.0.10 port=5432 dbname=appdb user=replicator password=your-secure-password'
PUBLICATION orders_pub_a
WITH (copy_data = false, create_slot = true, enabled = true, origin = none);
SQL
```

The `origin = none` parameter prevents each node from republishing changes it received via replication, which would cause an infinite loop.

### Step 6: Create the replicon configuration

```bash
replicon init -mode master-master > /etc/replicon/replicon.json
```

Edit `/etc/replicon/replicon.json`:

```json
{
  "cluster_name": "orders-prod",
  "mode": "master-master",
  "replication_user": "replicator",
  "node_a": {
    "name": "writer-a",
    "host": "10.0.0.10",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-a",
    "dsn_env": "REPLICON_NODE_A_DSN"
  },
  "node_b": {
    "name": "writer-b",
    "host": "10.0.0.11",
    "port": 5432,
    "data_dir": "/var/lib/postgresql/16/main",
    "postgres_user": "postgres",
    "ssh_user": "ubuntu",
    "server_id": "pg-b",
    "dsn_env": "REPLICON_NODE_B_DSN"
  },
  "logical": {
    "database": "appdb",
    "publication_a": "orders_pub_a",
    "publication_b": "orders_pub_b",
    "subscription_a": "orders_sub_a",
    "subscription_b": "orders_sub_b",
    "replication_cidr": "10.0.0.0/24"
  }
}
```

### Step 7: Set DSN environment variables

```bash
export REPLICON_NODE_A_DSN='postgres://postgres:dbpassword@10.0.0.10:5432/appdb?sslmode=require'
export REPLICON_NODE_B_DSN='postgres://postgres:dbpassword@10.0.0.11:5432/appdb?sslmode=require'
```

### Step 8: Validate, verify, and probe

```bash
replicon validate -config /etc/replicon/replicon.json
replicon verify -config /etc/replicon/replicon.json
replicon probe -config /etc/replicon/replicon.json
```

The probe tests both directions: writes on node-a appear on node-b, and writes on node-b appear on node-a.

---

## Part 5: SSH Setup For Failover Operations

The `promote` and `rejoin` commands execute remote commands over SSH. This section sets up key-based SSH access.

### Step 1: Generate an SSH key (if needed)

On the replicon host:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/replicon -N ''
```

### Step 2: Copy the key to both PostgreSQL servers

```bash
ssh-copy-id -i ~/.ssh/replicon ubuntu@10.0.0.10
ssh-copy-id -i ~/.ssh/replicon ubuntu@10.0.0.11
```

Replace `ubuntu` with the `ssh_user` from your config.

### Step 3: Configure sudo on the PostgreSQL servers

The SSH user needs passwordless sudo for PostgreSQL operations. On **both** servers:

```bash
sudo visudo -f /etc/sudoers.d/replicon
```

Add:

```
ubuntu ALL=(postgres) NOPASSWD: /usr/bin/psql, /usr/bin/pg_basebackup
ubuntu ALL=(root) NOPASSWD: /bin/systemctl stop postgresql, /bin/systemctl start postgresql, /bin/systemctl reload postgresql
```

Adjust paths if your PostgreSQL binaries are in a different location (e.g. `/usr/pgsql-16/bin/` on RHEL).

### Step 4: Verify SSH connectivity

```bash
replicon preflight -config /etc/replicon/replicon.json
```

Expected output:

```
  primary (10.0.0.10): OK
  standby (10.0.0.11): OK
SSH preflight: PASS
```

### Step 5: Dry-run a failover

Always dry-run first. This shows the commands that would be executed without running them:

```bash
replicon promote -config /etc/replicon/replicon.json
replicon rejoin -config /etc/replicon/replicon.json
```

### Step 6: Execute a failover (when needed)

```bash
replicon promote -config /etc/replicon/replicon.json -execute
```

After promotion, the old primary can be rejoined as a standby:

```bash
replicon rejoin -config /etc/replicon/replicon.json -execute
```

---

## Part 6: Running As A systemd Service

For continuous monitoring via the API, run replicon as a service.

### Step 1: Create the service user

```bash
sudo useradd --system --shell /usr/sbin/nologin --home-dir /opt/replicon replicon
sudo mkdir -p /opt/replicon
sudo mkdir -p /etc/replicon/tls
sudo mkdir -p /var/lib/replicon/audit
sudo chown replicon:replicon /var/lib/replicon/audit
```

### Step 2: Install the binary

```bash
sudo cp bin/replicon /usr/local/bin/replicon
sudo chmod 755 /usr/local/bin/replicon
```

### Step 3: Install the configuration

```bash
sudo cp replicon.json /etc/replicon/replicon.json
sudo chmod 640 /etc/replicon/replicon.json
sudo chown root:replicon /etc/replicon/replicon.json
```

### Step 4: Generate TLS certificates

For production use real certificates from your CA. For testing:

```bash
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout /etc/replicon/tls/server.key \
  -out /etc/replicon/tls/server.crt \
  -days 365 -nodes \
  -subj "/CN=replicon"

sudo chown replicon:replicon /etc/replicon/tls/server.key
sudo chmod 600 /etc/replicon/tls/server.key
```

### Step 5: Create the environment file

```bash
sudo tee /etc/replicon/replicon.env > /dev/null <<'EOF'
REPLICON_API_KEY=replace-with-long-random-token
REPLICON_PRIMARY_DSN=postgres://postgres:dbpassword@10.0.0.10:5432/postgres?sslmode=require
REPLICON_STANDBY_DSN=postgres://postgres:dbpassword@10.0.0.11:5432/postgres?sslmode=require
EOF

sudo chmod 600 /etc/replicon/replicon.env
sudo chown root:replicon /etc/replicon/replicon.env
```

### Step 6: Install the systemd unit

```bash
sudo cp deploy/replicon.service /etc/systemd/system/replicon.service
sudo systemctl daemon-reload
sudo systemctl enable --now replicon
```

### Step 7: Verify the service

```bash
sudo systemctl status replicon
curl -sk https://127.0.0.1:8443/healthz
curl -sk -H 'X-API-Key: replace-with-long-random-token' https://127.0.0.1:8443/readyz
curl -sk -H 'X-API-Key: replace-with-long-random-token' https://127.0.0.1:8443/api/v1/verify
```

---

## Part 7: Firewall Configuration

### UFW (Ubuntu)

```bash
# Allow PostgreSQL replication between servers
sudo ufw allow from 10.0.0.11 to any port 5432  # on server-a
sudo ufw allow from 10.0.0.10 to any port 5432  # on server-b

# Allow replicon API (if running service mode)
sudo ufw allow from 10.0.0.0/24 to any port 8443
```

### firewalld (RHEL / Rocky)

```bash
# Allow PostgreSQL replication
sudo firewall-cmd --permanent --add-rich-rule='rule family="ipv4" source address="10.0.0.11" port port="5432" protocol="tcp" accept'
sudo firewall-cmd --reload

# Allow replicon API
sudo firewall-cmd --permanent --add-rich-rule='rule family="ipv4" source address="10.0.0.0/24" port port="8443" protocol="tcp" accept'
sudo firewall-cmd --reload
```

---

## Part 8: Monitoring And Audit

### Prometheus metrics

If replicon is running in service mode, scrape `/metrics`:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: replicon
    scheme: https
    tls_config:
      insecure_skip_verify: true  # or use your CA
    authorization:
      credentials: replace-with-long-random-token
    static_configs:
      - targets: ['10.0.0.10:8443']
```

### Audit log

Review recent operations:

```bash
replicon history -audit-log /var/lib/replicon/audit/replicon.jsonl -limit 10
replicon history -audit-log /var/lib/replicon/audit/replicon.jsonl -output json
```

Or via the API:

```bash
curl -sk -H 'X-API-Key: replace-with-long-random-token' \
  'https://127.0.0.1:8443/api/v1/history?limit=10'
```

### Scheduled verification

Add a cron job to verify replication regularly:

```bash
# /etc/cron.d/replicon-verify
*/5 * * * * replicon /bin/bash -lc 'source /etc/replicon/replicon.env && /usr/local/bin/replicon verify -config /etc/replicon/replicon.json -audit-log /var/lib/replicon/audit/replicon.jsonl -output json >> /var/log/replicon-verify.log 2>&1'
```

---

## Troubleshooting

### replicon verify fails with "no rows in result set"

The standby is not connected to the primary. Check:

1. `pg_hba.conf` on the primary allows the standby IP
2. The replication slot exists: `SELECT * FROM pg_replication_slots;`
3. The standby can reach the primary on port 5432
4. The standby's `postgresql.auto.conf` has the correct `primary_conninfo`

### replicon probe fails with "probe row did not appear on standby"

Replication may be lagging or stalled. Check:

1. Replication state on the primary: `SELECT * FROM pg_stat_replication;`
2. Replay lag on the standby: `SELECT now() - pg_last_xact_replay_timestamp();`
3. Disk space on both servers
4. The DSN user has permission to create tables in the `public` schema

### replicon preflight fails

SSH connectivity is broken. Check:

1. The SSH user exists on the target server
2. Key-based authentication is configured
3. No firewall is blocking port 22
4. The SSH user has the correct sudo permissions

### Standby says "not in recovery"

The standby may have been accidentally promoted. Rebuild it:

1. Stop PostgreSQL on the standby
2. Re-run `pg_basebackup` from the primary (Step 4 in Part 3)
3. Start PostgreSQL on the standby

### Master-master subscription shows "down"

Check:

1. Both nodes have `wal_level = logical`
2. The replication user can connect to the publication database
3. `pg_hba.conf` allows the connection
4. The subscription is enabled: `SELECT subname, subenabled FROM pg_subscription;`
5. Restart a disabled subscription: `ALTER SUBSCRIPTION orders_sub_a ENABLE;`

---

## Quick Reference

| Task | Command |
|------|---------|
| Generate sample config | `replicon init -mode master-slave` |
| Validate config | `replicon validate -config replicon.json` |
| Show execution plan | `replicon plan -config replicon.json` |
| Render primary config | `replicon render -config replicon.json -target primary` |
| Render standby config | `replicon render -config replicon.json -target standby` |
| Check replication state | `replicon verify -config replicon.json` |
| Active replication test | `replicon probe -config replicon.json` |
| SSH connectivity check | `replicon preflight -config replicon.json` |
| Dry-run promote | `replicon promote -config replicon.json` |
| Execute promote | `replicon promote -config replicon.json -execute` |
| Dry-run rejoin | `replicon rejoin -config replicon.json` |
| Execute rejoin | `replicon rejoin -config replicon.json -execute` |
| View audit history | `replicon history -audit-log path.jsonl` |
| Start API service | `replicon serve -config replicon.json -tls-cert cert.pem -tls-key key.pem` |
| JSON output | Add `-output json` to any command |
