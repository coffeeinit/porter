# Image and Build Pipeline

## Core rule

Porter's canonical deployable artifact is an **OCI image digest**.

Everything else is an input adapter:

```text
Direct MicroVM image ───────────────► rootfs.ext4 + vmlinux
Dockerfile ───────► BuildKit OCI ───► rootfs.ext4 + vmlinux
docker-compose.yml ─► OCI service ──► rootfs.ext4 + vmlinux
OCI image ──────────────────────────► rootfs.ext4 + vmlinux
Docker image ───────────────────────► rootfs.ext4 + vmlinux
GitHub source ─► framework detect ─► generated Dockerfile ─► BuildKit OCI
```

Docker/Compose are **build inputs**, never the customer runtime.

## 1. Direct MicroVM image

User supplies:

```text
name
kernel (vmlinux)
rootfs (rootfs.ext4)
arch
```

Porter validates:

- kernel magic/size
- ext4 filesystem
- arch match
- SHA-256 digests
- optional boot test

Result:

```text
image type: microvm
artifact: kernel + rootfs
```

This is the fastest path and useful for prebuilt images.

## 2. Dockerfile

Flow:

```text
repo/context
  -> BuildKit
  -> OCI image layout
  -> digest
  -> unpack layers with whiteouts
  -> merge with Porter guest base
  -> install porter-guest-init
  -> mkfs.ext4
  -> image record
```

Inputs:

```text
git repo or upload
dockerfile path
build context
build args
secrets
target stage
```

Result:

```text
image type: oci
artifact: rootfs.ext4 + shared kernel
```

## 3. docker-compose.yml

Compose is an orchestration input, not a portable runtime.

V1 recommendation:

- parse compose file
- support **single primary service** for first deployment
- each service with `build:` builds an OCI image
- each service with `image:` pulls an OCI image
- sidecars/databases become additional Porter services later
- never run Docker Compose on the host

Compose example:

```yaml
services:
  web:
    build: .
    ports:
      - "3000:3000"
  db:
    image: postgres:17
```

V1 build target = `web`.

P1: create a multi-service project with `web` + `db` MicroVMs, private network, and service discovery.

## 4. OCI image / Docker image

Both use the same path:

```text
reference
  -> resolve manifest
  -> pull layers
  -> verify digest
  -> apply layers with whiteouts
  -> merge onto guest base
  -> install porter-guest-init
  -> mkfs.ext4
```

Supported references:

```text
docker.io/library/nginx:1.29-alpine
ghcr.io/org/app@sha256:...
oci://registry/...
```

The result is immutable by digest.

## 5. GitHub source + framework detection

GitHub is a **source adapter**, not a separate runtime.

```text
GitHub repo / branch / commit
  -> detect framework
  -> choose/generate Dockerfile
  -> BuildKit build
  -> OCI image
  -> rootfs.ext4
```

### Laravel / PHP

Detect:

```text
composer.json
artisan
public/index.php
```

Image model:

- php-fpm
- nginx or Caddy sidecar
- app source in `/var/www/html`

### React / Vite

Detect:

```text
package.json
vite.config.*
react dependency
```

Build:

- `npm ci`
- `npm run build`
- serve `dist/` with nginx

### Next.js

Detect:

```text
next.config.*
next dependency
```

Build:

- `npm ci`
- `npm run build`
- run `next start` under Node in the MicroVM

### Generic Node / Express / Nest

Detect:

```text
package.json
```

Build:

- install dependencies
- run `npm start` or detected start script

### Static HTML

Detect:

```text
index.html
```

Build:

- copy static files
- serve with nginx

Framework detection must be explicit. If detection is uncertain, the UI asks
the user to choose a framework or provide a Dockerfile.

## 6. Generated Dockerfiles vs user Dockerfile

Priority:

1. user Dockerfile in repo
2. user Dockerfile path in project settings
3. framework template
4. fail with a clear message

Porter should not guess silently.

## 7. Guest base image

Every OCI app image needs a compatible guest base containing:

- kernel module expectations
- `porter-guest-init`
- networking tools
- CA certificates
- basic init/entrypoint handling

The app OCI layers are merged onto this base.

## 8. Runtime rootfs preparation

After OCI unpack:

- apply whiteouts
- set entrypoint/cmd/env/workdir
- install `porter-guest-init`
- add MMDS network config
- create ext4 with `mkfs.ext4 -d`
- verify digest and size

## 9. Image record

```sql
images(
  id,
  name,
  source_type,      -- microvm | dockerfile | compose | oci | docker | git
  source_ref,
  digest,
  kernel_ref,
  rootfs_ref,
  arch,
  status,           -- queued|building|ready|failed
  size_bytes,
  error,
  created_at
)
```

Deployments reference an immutable image digest, never a mutable tag.

## 10. Recommended V1 scope

P0:

- direct MicroVM image
- OCI/Docker image import
- user Dockerfile via BuildKit
- GitHub repo + Dockerfile
- generated Dockerfile for Laravel / Next.js / React / Node / static
- single-service compose primary service

P1:

- full compose multi-service stacks
- private service networking
- build cache
- SBOM + vulnerability scan
- image signing

## 11. What not to do

- do not run Docker Compose on the host as runtime
- do not boot an OCI image directly as a VM
- do not treat Docker as the isolation boundary
- do not rely on mutable image tags
- do not silently guess an unknown framework
