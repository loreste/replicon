#!/bin/sh
set -eu

cat >> "${PGDATA}/pg_hba.conf" <<'EOF'
host replication replicator all scram-sha-256
host all all all scram-sha-256
EOF

psql -v ON_ERROR_STOP=1 -U postgres -d postgres <<EOF
SELECT pg_reload_conf();
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'replicator') THEN
    CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD '${REPLICATION_PASSWORD}';
  END IF;
END
\$\$;
SELECT pg_create_physical_replication_slot('${REPLICATION_SLOT}')
WHERE NOT EXISTS (
  SELECT 1
  FROM pg_replication_slots
  WHERE slot_name = '${REPLICATION_SLOT}'
);
EOF
