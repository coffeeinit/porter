# Porter backend — build, run, dev, test, clean (self-contained; no need to cd
# to the repo root). The binary embeds web/dist, so build the frontend first:
#   make frontend    (npm install + vite build -> web/dist)
#   make build
#
# Entrypoint is cmd/porter (`porter server|worker|kernel|version`).

.PHONY: frontend build builder-image convert jailer-test run dev test vet clean

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo v0.1.0-beta-dev)
DB_URL  ?= postgres://porter:porter@localhost:5432/porter?sslmode=disable

frontend:
	cd web && npm install && npm run build

build:
	go build -trimpath -ldflags "-X main.Version=$(VERSION)" -o bin/porter ./cmd/porter

# One-time bootstrap: requires a Firecracker-compatible vmlinux and a BuildKit
# OCI archive. Runtime Git builds use the resulting builder MicroVM; Docker and
# Podman are not runtime dependencies.
builder-image:
	@test -n "$(KERNEL)" -a -n "$(OCI)" || (echo 'usage: make builder-image KERNEL=/path/to/vmlinux OCI=/path/to/buildkit.oci.tar' >&2; exit 2)
	bash scripts/build-builder-image.sh --kernel "$(KERNEL)" --oci "$(OCI)"

convert:
	go build -trimpath -o bin/porter-convert ./cmd/porter-convert

jailer-test:
	go test ./internal/jailer ./internal/runtime

run: build
	./bin/porter server $(ARGS)

# Local dev — one-command dev environment (Docker Postgres + simulate mode).
dev:
	bash ../scripts/backend/dev.sh up

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin porter porter.db
