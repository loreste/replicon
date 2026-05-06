#!/bin/sh
set -eu

psql -v ON_ERROR_STOP=1 -U postgres -d appdb <<'SQL'
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'replicator') THEN
    CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'postgres';
  END IF;
END
$$;

GRANT ALL ON SCHEMA public TO replicator;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO replicator;
SQL
