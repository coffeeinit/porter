#!/usr/bin/env bash
# Restart the porter demo server (port 8080, porter_demo DB). Self-contained:
# kills by exact stored pidfile so the pattern can never match its own caller.
set -e
PIDFILE=/tmp/porter-demo-runs.pid
if [ -f "$PIDFILE" ]; then
  kill "$(cat "$PIDFILE")" 2>/dev/null || true
  sleep 1
fi
cd /mnt/d/github/porter/backend
go build -o /tmp/porter.demo.bin ./cmd/porter
export PORTER_DATABASE_URL='postgres://porter:porter@localhost:5433/porter_demo?sslmode=disable'
export PORTER_BOOTSTRAP_ADMIN_PASSWORD='porter-demo-2026'
export PORTER_SECRET_KEY='demo-secret-key-0123456789abcdef0123456789abcdef'
export PORTER_LISTEN_ADDR=':8080'
setsid nohup /tmp/porter.demo.bin server > /tmp/porter-demo-runs.log 2>&1 < /dev/null &
echo $! > "$PIDFILE"
sleep 8
curl -s -m 5 -o /dev/null -w "HTTP=%{http_code}\n" http://localhost:8080/api/v1/csrf
