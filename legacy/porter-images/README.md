# Porter Image Catalog

Source of truth, build definitions, and browsable catalog for
[Porter](https://github.com/vmforge/porter) MicroVM images.

- **Git holds metadata** — image definitions, schemas, converters, `catalog.json`.
- **GitHub Releases hold artifacts** — `rootfs.ext4`, kernels, checksums (never committed).
- **GitHub Pages** (`index.html` + `catalog.json`) is the zero-backend browsable UI.
- **Porter resolves** `porter image install nginx` → `catalog.json` → build manifest → artifact URL → download → SHA256 verify → local image catalog → Firecracker boot.

## Layout

```
index.html            GitHub Pages UI (static, fetches catalog.json)
catalog.json          generated — do not edit by hand
schema/               image.schema.json, manifest.schema.json
images/<name>/image.json   one declarative definition per image
converters/
  docker-to-rootfs/   OCI pull -> unpacked rootfs tree (whiteouts applied)
  rootfs-to-ext4/     base + app merge -> rootfs.ext4
  validate-image/     schema validation + catalog.json regeneration
bases/                Porter base roots (kernel + init + guest runtime)
.github/workflows/    validate + build CI
```

## Adding an image

1. Create `images/<name>/image.json` following `schema/image.schema.json`.
2. Run `python converters/validate-image/validate.py` (needs `pip install jsonschema`).
3. Commit — CI revalidates and builds.

### Where an image comes from (`source.kind`)

| kind | reference | how it becomes a microvm |
|---|---|---|
| `docker-hub` / `ghcr` / `oci` | OCI reference without tag (`docker.io/library/nginx`) | pull + unpack layers → merge onto base → rootfs.ext4 |
| `github-release` | `owner/repo` | download prebuilt kernel/rootfs release artifacts |
| `dockerfile` | path to `Dockerfile` in this image's dir | `docker build` → the same pull+unpack path |
| `compose` | path to `compose.yml` in this image's dir | resolve each service → build/pull → same path |

Tags are common, upstream tags (`nginx:1.29-alpine`) — the same reference works whether the consumer uses Docker, plain OCI tooling, or Porter.

### Example: building from your own Dockerfile

```
images/myapp/
├── image.json     # {"source": {"kind": "dockerfile", "reference": "images/myapp/Dockerfile"}, ...}
└── Dockerfile
```

Every `source.kind` funnels into the same OCI-unpack → whiteout → base-merge → ext4 pipeline, so an application image always lands as a Firecracker-bootable rootfs regardless of where it started.

## Convert locally

```bash
python converters/docker-to-rootfs/convert.py docker.io/library/nginx:1.29-alpine -o /tmp/rootfs
sudo python converters/rootfs-to-ext4/pack.py --base bases/alpine/rootfs-tree --app /tmp/rootfs --out nginx.ext4
```

## Status

- [x] Catalog UI + generated `catalog.json`
- [x] JSON schemas for image definitions and build manifests
- [x] OCI pull/unpack prototype (whiteouts applied)
- [x] ext4 packing prototype (requires e2fsprogs)
- [ ] Porter base roots (kernel + guest runtime) in `bases/`
- [ ] GitHub Actions build → Release artifact publishing
- [ ] `porter image install` client wired to this catalog
