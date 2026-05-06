ARG PG_VERSION=16
FROM postgres:${PG_VERSION}

COPY integration/primary-init.sh /docker-entrypoint-initdb.d/00-primary-init.sh

RUN chmod 755 /docker-entrypoint-initdb.d/00-primary-init.sh
