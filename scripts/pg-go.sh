#!/usr/bin/env bash
# Porter dev (WSL): make sure the dockerized Postgres is up, then run a Go
# command with PG-gated tests enabled.
#
#   scripts/pg-go.sh go build ./...
#   scripts/pg-go.sh go test -count=1 ./internal/store/
#
# Container default: porter-dev-pg (postgres:16, porter/porter) on host port
# 5433 with databases porter + porter_test. Override with PORTER_PG_CONTAINER
# and PORTER_TEST_DATABASE_URL.
set -euo pipefail

CONTAINER="${PORTER_PG_CONTAINER:-porter-dev-pg}"

docker start "$CONTAINER" >/dev/null 2>&1 || true
for _ in $(seq 1 30); do
	docker exec "$CONTAINER" pg_isready -U porter >/dev/null 2>&1 && break
	sleep 1
done
docker exec "$CONTAINER" psql -U porter -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='porter_test'" | grep -q 1 ||
	docker exec "$CONTAINER" psql -U porter -d postgres -c "CREATE DATABASE porter_test" >/dev/null

export PORTER_TEST_DATABASE_URL="${PORTER_TEST_DATABASE_URL:-postgres://porter:porter@localhost:5433/porter_test?sslmode=disable}"
cd "$(dirname "$0")/.."
exec "$@"
