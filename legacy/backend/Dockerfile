# Porter control plane — single Go binary + migrate on boot.
# Build:  docker build -t porter:local ./backend
# Run:    docker run --privileged -v /dev/kvm:/dev/kvm -v porter-data:/var/lib/porter \
#           -e PORTER_DATABASE_URL=... -e PORTER_SECRET_KEY=... -p 8080:8080 porter:local
# --privileged + /dev/kvm are required on the VM hosts (KVM + TAP + jailer).
# PostgreSQL and Redis run outside this image (managed PG recommended).

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# web/dist must exist (make frontend) — it is embedded into the binary.
RUN CGO_ENABLED=0 go build -trimpath -o /out/porter ./cmd/porter

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates iproute2 iptables nftables e2fsprogs kmod \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/porter /usr/local/bin/porter
RUN mkdir -p /run/porter/firecracker /var/lib/porter/snapshots /var/log/porter
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/porter"]
CMD ["server"]
