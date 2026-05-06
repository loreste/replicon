# Deployment

replicon can be deployed as:

- a **standalone CLI** on any machine with network access to your PostgreSQL servers
- a **TLS-protected long-running admin API** (service mode)
- a **container image**
- a **systemd-managed service** on a bare-metal or VM host

It works the same way regardless of where PostgreSQL itself runs — Docker, bare-metal, cloud VMs, or managed services (as long as you have DSN access and, for failover operations, SSH access).

If you need the basic install paths first, see [Installation](./installation.md).

## Bare-Metal / VM Deployment

This is the most common production setup. replicon runs on a management host (or directly on one of the PostgreSQL servers) and connects to the databases over the network.

### 1. Install the binary

Build from source or use a pre-built release binary:

```bash
# From source
make build
sudo cp bin/replicon /usr/local/bin/replicon

# Or cross-compile for a remote Linux server
make package-release
scp dist/replicon-linux-amd64 admin@mgmt-host:/usr/local/bin/replicon
```

### 2. Create the configuration

```bash
replicon init -mode master-slave > /etc/replicon/replicon.json
```

Edit the config to match your cluster. Use `dsn_env` (not `dsn`) so credentials stay in environment variables rather than the config file.

### 3. Set up environment variables

```bash
# /etc/replicon/replicon.env
REPLICON_PRIMARY_DSN=postgres://postgres:secret@primary-host:5432/postgres?sslmode=require
REPLICON_STANDBY_DSN=postgres://postgres:secret@standby-host:5432/postgres?sslmode=require
REPLICON_API_KEY=replace-with-long-random-token
```

### 4. Set up SSH (for promote/rejoin only)

```bash
ssh-copy-id ubuntu@primary-host
ssh-copy-id ubuntu@standby-host
replicon preflight -config /etc/replicon/replicon.json
```

### 5. Validate and verify

```bash
source /etc/replicon/replicon.env
replicon validate -config /etc/replicon/replicon.json
replicon verify -config /etc/replicon/replicon.json
replicon probe -config /etc/replicon/replicon.json
```

## Container Deployment

Build the image:

```bash
docker build -t replicon:local .
```

Run it:

```bash
docker run --rm \
  -p 8443:8443 \
  --env-file deploy/replicon.env.example \
  -v "$(pwd)/replicon.json:/etc/replicon/replicon.json:ro" \
  -v "$(pwd)/tls:/etc/replicon/tls:ro" \
  replicon:local \
  serve \
  -config /etc/replicon/replicon.json \
  -listen :8443 \
  -tls-cert /etc/replicon/tls/server.crt \
  -tls-key /etc/replicon/tls/server.key \
  -audit-log /tmp/replicon-audit.jsonl
```

## systemd Service

Files included:

- `deploy/replicon.service`
- `deploy/replicon.env.example`

Install flow:

1. Copy the binary to `/usr/local/bin/replicon`
2. Copy your config to `/etc/replicon/replicon.json`
3. Copy TLS assets to `/etc/replicon/tls/`
4. Copy the env file to `/etc/replicon/replicon.env`
5. Copy the unit file to `/etc/systemd/system/replicon.service`
6. Create the service user and audit directory:

```bash
sudo useradd --system --shell /usr/sbin/nologin replicon
sudo mkdir -p /var/lib/replicon/audit
sudo chown replicon:replicon /var/lib/replicon/audit
```

7. Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now replicon
sudo systemctl status replicon
```

## Environment Variables

At minimum, provide:

- `REPLICON_API_KEY` (required for service mode)
- `REPLICON_PRIMARY_DSN` and `REPLICON_STANDBY_DSN` for master-slave
- or `REPLICON_NODE_A_DSN` and `REPLICON_NODE_B_DSN` for master-master

## PostgreSQL Version Support

replicon has been tested on PostgreSQL 13, 14, and 16. It uses standard replication features (`pg_stat_replication`, `pg_basebackup`, physical and logical replication slots) that are stable across these versions.

## Operational Notes

- Keep TLS keys out of the repo.
- Replace the example env file with a real secret-managed file before production.
- Use a dedicated service account instead of running as root.
- Put the service behind firewall rules even though it already requires TLS and API-key auth.
- For `promote` and `rejoin`, the replicon host needs SSH access to the PostgreSQL servers. Use key-based auth and run `replicon preflight` to verify connectivity before attempting failover.
