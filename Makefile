VERSION ?= v1.0.0-dev
BIN := bin

.PHONY: all build test vet fmt clean porter agent

all: build

build: porter agent

porter:
	cd porter && go build -trimpath -ldflags "-X main.Version=$(VERSION)" -o ../$(BIN)/porter ./cmd/porter

agent:
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
