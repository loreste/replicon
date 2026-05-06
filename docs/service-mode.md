# Service Mode

`replicon serve` runs the tool as a TLS-protected admin API instead of a one-shot CLI.

## What It Exposes

- `GET /healthz`
- `GET /readyz`
- `GET /metrics`
- `GET /api/v1/history?limit=20`
- `GET` or `POST /api/v1/validate`
- `GET` or `POST /api/v1/verify`
- `GET` or `POST /api/v1/probe`
- `POST /api/v1/promote`
- `POST /api/v1/rejoin`

`/metrics` and `/api` endpoints require authentication.

## Requirements

- a config file
- a TLS certificate and private key
- an API key in an environment variable
- DSN environment variables for the configured nodes

By default the API key environment variable is `REPLICON_API_KEY`.

## Start The Service

```bash
export REPLICON_API_KEY='replace-with-long-random-token'
export REPLICON_PRIMARY_DSN='postgres://postgres:secret@10.0.0.10:5432/postgres?sslmode=require'
export REPLICON_STANDBY_DSN='postgres://postgres:secret@10.0.0.11:5432/postgres?sslmode=require'

go run . serve \
  -config replicon.json \
  -listen :8443 \
  -tls-cert server.crt \
  -tls-key server.key \
  -audit-log var/audit/replicon.jsonl
```

## Authentication

Use either:

- `X-API-Key: <token>`
- `Authorization: Bearer <token>`

Example:

```bash
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
```

## JSON Responses

The API returns structured JSON with:

- action
- cluster
- mode
- status
- summary
- error
- started and finished timestamps
- duration
- sanitized details

Sensitive values like DSNs and passwords are redacted before the response is written.

## Readiness And Metrics

`/readyz` validates that the configured cluster definition is usable. This is stronger than `/healthz`, which only proves the process is up.

`/metrics` exposes Prometheus-style counters for:

- total command runs by action and status
- total command duration by action

## Audit Logging

Every validate, verify, probe, promote, and rejoin request is appended to the configured JSONL audit log.

You can also read recent entries back through `/api/v1/history`.

Use this for:

- operator audit trails
- troubleshooting
- basic run history

## Deployment Notes

- Put this behind a firewall or internal load balancer even though it already requires API-key auth.
- Use real certificates, not self-signed certs, in production.
- Restrict which operators or automation can call `probe`, since it performs real writes.
