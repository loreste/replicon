#!/bin/sh
set -eu

if [ ! -s "${PGDATA}/PG_VERSION" ]; then
  rm -rf "${PGDATA:?}"/*
  export PGPASSWORD="${REPLICATION_PASSWORD}"
  until pg_basebackup \
    --host="${PRIMARY_HOST}" \
    --port="${PRIMARY_PORT}" \
    --username="${REPLICATION_USER}" \
    --pgdata="${PGDATA}" \
    --write-recovery-conf \
    --slot="${REPLICATION_SLOT}" \
    --checkpoint=fast \
    --progress
  do
    sleep 1
  done

  {
    echo "primary_slot_name = '${REPLICATION_SLOT}'"
    echo "primary_conninfo = 'host=${PRIMARY_HOST} port=${PRIMARY_PORT} user=${REPLICATION_USER} application_name=${APPLICATION_NAME}'"
    echo "hot_standby = on"
  } >> "${PGDATA}/postgresql.auto.conf"
fi

exec docker-entrypoint.sh postgres
