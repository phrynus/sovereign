#!/bin/bash
# CI port of the demo restore drill. Everything runs through `docker exec`, so
# the runner needs no local psql/pg_dump. It mirrors deploy/demo/restore-drill.sh:
# dump the live database, restore into a scratch database, compare row counts on
# the six tables the demo drill audits, then drop the scratch database.
#
#   bash scripts/ci-pg-restore-drill.sh [dump.sql.gz]
#
# Requires a running PostgreSQL container named forgec2-pg with the forgec2
# role/database (the same shape the CI smoke and E2E jobs start).
set -euo pipefail

PG_CONTAINER="${PG_CONTAINER:-forgec2-pg}"
PG_ROLE="${PG_ROLE:-forgec2}"
PG_DB="${PG_DB:-forgec2}"
SCRATCH="${PG_DRILL_DB:-forgec2_drill_restore}"
DUMP="${1:-${RUNNER_TEMP:-/tmp}/forgec2-drill.sql.gz}"
TABLES="users implants tasks tenants agent_groups agent_tags"

pg() { docker exec "$PG_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$PG_ROLE" "$@"; }
pg_live() { pg -d "$PG_DB" "$@"; }

# Seed deterministic rows: a dump that silently exports nothing must not pass
# by comparing two empty tables.
for table in $TABLES; do
  pg_live -q -c "CREATE TABLE IF NOT EXISTS $table (id serial PRIMARY KEY)"
  pg_live -q -c "INSERT INTO $table DEFAULT VALUES"
done

echo "drill dump: $DUMP"
docker exec "$PG_CONTAINER" pg_dump -U "$PG_ROLE" -d "$PG_DB" --no-owner --no-privileges | gzip > "$DUMP"

pg -d postgres -q -c "DROP DATABASE IF EXISTS $SCRATCH"
docker exec "$PG_CONTAINER" createdb -U "$PG_ROLE" -O "$PG_ROLE" "$SCRATCH"
gzip -dc "$DUMP" | docker exec -i "$PG_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$PG_ROLE" -d "$SCRATCH" -q > /dev/null
echo "restore: ok"

status=0
for table in $TABLES; do
  live=$(pg_live -tAc "SELECT COUNT(*) FROM $table" | tr -d '[:space:]')
  restored=$(pg -d "$SCRATCH" -tAc "SELECT COUNT(*) FROM $table" | tr -d '[:space:]')
  echo "table $table: live=$live restored=$restored"
  if [ "$live" != "$restored" ]; then
    echo "MISMATCH on $table" >&2
    status=1
  fi
done

docker exec "$PG_CONTAINER" dropdb -U "$PG_ROLE" "$SCRATCH" || true
if [ "$status" -ne 0 ]; then
  echo "restore drill: FAIL" >&2
  exit "$status"
fi
echo "restore drill: PASS (counts match)"
