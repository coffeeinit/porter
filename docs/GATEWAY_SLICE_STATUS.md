# Gateway Slice — Status

## Completed

- host-based gateway routing
- weighted replica selection using `traffic_weight`
- health-aware filtering (`running` + `healthy|checking`)
- request logging: host, path, status, latency, bytes, replica
- request activity updates app autoscale signal
- domain management and auto `<app>.<base-domain>`
- automatic preview domain API (`pr-<n>-<app>.<base-domain>`)
- optional TLS listener via `PORTER_TLS_CERT` / `PORTER_TLS_KEY`
- gateway stats and request-log APIs
- MCP tools for domains, previews, gateway stats, and fleet detail
- fleet node detail APIs: node, metrics, logs, VMs, cordon/drain
- `porter-guest-init` binary scaffold:
  - reads `porter.ip/gw/mask/dns` from `/proc/cmdline`
  - configures `eth0`, route, DNS
  - execs the app entrypoint

## Still required

- real ACME/Let's Encrypt automation (TLS cert files are supported now)
- wildcard certificate/DNS-01
- health checks driven by guest HTTP/TCP probes
- canary by deployment version (weights exist per replica)
- WebSocket/gRPC gateway support
- bandwidth charts/trace waterfall
- guest-init injection into every rootfs (image pipeline)
- full multi-page UI
- image/build pipeline
