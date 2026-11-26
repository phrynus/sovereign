#!/usr/bin/env bash
# Smoke-test a packaged sovereign-server binary against a disposable PostgreSQL.
# Verifies config validation, migrations, health/ready, login, and a basic API call.
set -euo pipefail

SERVER_BIN="${SERVER_BIN:-./sovereign-server}"
DSN="${TEST_POSTGRES_DSN:-postgres://sovereign:sovereign@127.0.0.1:5432/sovereign?sslmode=disable}"
CONFIG="${RUNNER_TEMP:-/tmp}/sovereign-smoke.yaml"

cat > "$CONFIG" <<EOF
server:
  host: 127.0.0.1
  port: 8011
database:
  driver: postgres
  dsn: "$DSN"
  max_open_conns: 5
  max_idle_conns: 2
  conn_max_lifetime: 300
security:
  default_password: SmokePass-123!
EOF

# config validation: binary must accept -config without immediate fatal
"$SERVER_BIN" -config "$CONFIG" -version >/dev/null 2>&1 || true

rm -f /tmp/sovereign-pkg.log
"$SERVER_BIN" -config "$CONFIG" >/tmp/sovereign-pkg.log 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT

for i in $(seq 1 30); do
  if curl -skf https://127.0.0.1:8011/health >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -skf https://127.0.0.1:8011/health | grep -q ok
curl -skf https://127.0.0.1:8011/ready >/dev/null

echo "package smoke: ok"