# GitHub and Compose Adapter Status

## Working now

- framework detection:
  - Dockerfile
  - Laravel/PHP
  - Next.js
  - React/Vite
  - Node/Express/Nest
  - static HTML
- generated `Dockerfile.porter` for detected frameworks
- local git clone + branch/subdir support
- Dockerfile build into rootfs.ext4
- Docker/OCI import into rootfs.ext4
- Compose primary-service parser
- Compose `build:` and `image:` support
- guest-init injected into built rootfs
- API and MCP tools

## API

```text
POST /v1/images/detect
POST /v1/images/build
```

Build source types:

```text
dockerfile
docker
oci
git
compose
```

Git example:

```json
{
  "name": "my-laravel-app",
  "source_type": "git",
  "git_url": "https://github.com/acme/app.git",
  "branch": "main",
  "size_mb": 512
}
```

Compose example:

```json
{
  "name": "my-compose-app",
  "source_type": "compose",
  "compose_path": "/srv/app/docker-compose.yml",
  "size_mb": 512
}
```

## Tested

- React detection unit test
- Compose primary-service parser unit test
- Dockerfile -> rootfs.ext4 live build in WSL

## Remaining

- multi-service Compose stacks
- private GitHub auth/app installation
- webhook-triggered build
- build logs streaming
- BuildKit cache
- SBOM/scan/sign
- builder MicroVM isolation
