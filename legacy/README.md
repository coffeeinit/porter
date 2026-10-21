# Legacy archive

Everything here is archived reference material. It is **not** the target V1
implementation and should not be imported by the new `porter/` or `fleet/`
projects.

## Contents

| Path | What it is |
|---|---|
| `backend/` | Old monolithic Go control plane/agent prototype |
| `SRS_V1.md` | Old full SRS |
| `CLAUDE.md` | Old agent/implementation contract |
| `CHANGELOG.md` | Old backend changelog |
| `PORTER_COMPLETE_PRODUCT_AUDIT(1).md` | Old product/API audit |
| `docs-v2/` | Prior `README.md`, `SRS_V2.md`, `IMPLEMENTATION_PLAN_V2.md` |
| `prototypes/` | Old single-file HTML UI prototypes |
| `porter-images/` | Old standalone image-catalog project |

## What to keep from here

Useful lessons/references:

- Firecracker API sequence and boot parameters
- OCI -> rootfs.ext4 conversion logic
- migration/domain ideas
- status labels and controller concepts
- old UI information architecture

Do not copy:

- the 845-route API surface
- customers/billing/audit tables
- runtime/resource type duplication
- large monolithic packages
- fake/501 handlers
- the old monolithic DB schema
