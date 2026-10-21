# Image Build Pipeline — Status

## Working now

### Dockerfile -> rootfs.ext4
- `docker build`
- `docker save` (OCI layout or Docker save)
- layer unpack with whiteouts
- gzip layer support
- guest-init injection at `/sbin/porter-guest-init`
- `mkfs.ext4 -d`
- SHA-256 digest
- image catalog record
- API/MCP build tools

Verified in WSL:

```text
Dockerfile -> docker build -> OCI tar -> rootfs.ext4
image: img_...
digest: sha256:8355...
size: 64 MiB
status: ready
```

### Docker/OCI image -> rootfs.ext4
- `docker pull`
- `docker tag`
- `docker save`
- same conversion path

## API

```text
POST /v1/images/build
{
  "name": "my-app",
  "source_type": "dockerfile",
  "context_dir": "/path/to/app",
  "dockerfile": "/path/to/app/Dockerfile",
  "size_mb": 512
}
```

or:

```json
{
  "name": "nginx",
  "source_type": "oci",
  "source_ref": "docker.io/library/nginx:1.29-alpine"
}
```

## MCP

```text
porter_build_image
porter_list_images
porter_create_image
```

## Remaining

- Compose parser and primary-service build
- GitHub clone + framework detection:
  - Laravel/PHP
  - React/Vite
  - Next.js
  - Node/Express/Nest
  - static HTML
- Build cache
- SBOM/vulnerability scanning
- image signing
- builder isolation in a MicroVM instead of host Docker
