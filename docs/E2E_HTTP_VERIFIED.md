# First E2E App Runtime — Verified

Date: 2026-10-19

## Verified path

```text
Dockerfile (nginx:alpine)
  -> docker build
  -> OCI layout
  -> rootfs.ext4 + guest-init
  -> porter-agent boots Firecracker
  -> guest network configured (10.42.0.2)
  -> /sbin/porter-guest-init runs
  -> /porter/entrypoint launches nginx
  -> agent HTTP probe succeeds
  -> replica health = healthy
  -> gateway route by domain
  -> curl returns app response
```

## Test command

```bash
sudo bash scripts/e2e-http.sh
```

## Evidence

```text
HEALTHY
== gateway response ==
porter-nginx-ok
```

## What this proves

- image build pipeline works end-to-end
- guest-init injection works
- guest network setup works
- OCI entrypoint/cmd execution works
- agent HTTP health reporting works
- Porter health state works
- domain gateway routing works
- users never need VM IP/port

This is the first real Porter app-runtime milestone.
