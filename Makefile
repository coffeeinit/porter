VERSION ?= v1.0.0-dev
BIN := bin
DESTDIR ?=
SYSTEMD_UNIT_DIR ?= /etc/systemd/system

.PHONY: all build install test vet fmt clean porter agent porter-migrate

all: build

build: porter agent porter-migrate

install: porter porter-migrate
	@if [ -z "$(DESTDIR)" ]; then \
		command -v systemd >/dev/null || { echo "systemd >= 247 is required" >&2; exit 1; }; \
		version="$$(systemd --version | awk 'NR == 1 {print $$2}')"; \
		case "$$version" in ''|*[!0-9]*) echo "could not determine systemd version" >&2; exit 1;; esac; \
		[ "$$version" -ge 247 ] || { echo "systemd >= 247 is required" >&2; exit 1; }; \
	fi
	install -D -m 0755 $(BIN)/porter $(DESTDIR)/usr/local/bin/porter
	install -D -m 0755 $(BIN)/porter-migrate $(DESTDIR)/usr/local/bin/porter-migrate
	install -D -m 0644 deploy/systemd/porter.service $(DESTDIR)$(SYSTEMD_UNIT_DIR)/porter.service
	install -D -m 0644 deploy/systemd/porter-migrate.service $(DESTDIR)$(SYSTEMD_UNIT_DIR)/porter-migrate.service
	@if [ -z "$(DESTDIR)" ]; then systemctl daemon-reload; fi

porter:
	mkdir -p $(BIN)
	cd porter && go build -trimpath -ldflags "-X main.Version=$(VERSION)" -o ../$(BIN)/porter ./cmd/porter

porter-migrate:
	mkdir -p $(BIN)
	cd porter && go build -trimpath -o ../$(BIN)/porter-migrate ./cmd/porter-migrate

agent:
	mkdir -p $(BIN)
	cd porter-agent && go build -trimpath -ldflags "-X main.Version=$(VERSION)" -o ../$(BIN)/porter-agent ./cmd/porter-agent
	cd porter-agent && go build -trimpath -ldflags "-X main.Version=$(VERSION)" -o ../$(BIN)/porter-guest-init ./cmd/porter-guest-init

test:
	cd protocol && go test ./...
	cd porter && go test ./...
	cd porter-agent && go test ./...

vet:
	cd protocol && go vet ./...
	cd porter && go vet ./...
	cd porter-agent && go vet ./...

fmt:
	gofmt -w protocol porter porter-agent

clean:
	rm -rf $(BIN)

.PHONY: e2e-http e2e-snapshot e2e-mcp kvm-acceptance
e2e-http:
	@echo "Disabled: legacy E2E used shared /tmp paths and pkill; see scripts/README.md" >&2
	@false
e2e-snapshot:
	@echo "Disabled: legacy E2E used shared /tmp paths and pkill; see scripts/README.md" >&2
	@false
e2e-mcp:
	@echo "Disabled: legacy E2E used shared /tmp paths and pkill; see scripts/README.md" >&2
	@false

kvm-acceptance:
	cd porter-agent && PORTER_KVM_ACCEPTANCE=1 go test -count=1 -run '^TestKVMFirecrackerApplyStopAcceptance$$' -v ./internal/runtime
