# Persistence Model

## Three different things

Do not confuse these:

1. **Build artifact** — immutable OCI image / rootfs. Always rebuilt only on deploy.
2. **Runtime filesystem** — ephemeral local rootfs/scratch inside the VM.
3. **Persistent data** — database files, uploads, user content.

## What "ephemeral" means

Ephemeral does **not** mean the site is rebuilt on every request.

It means:

> When this MicroVM stops or is redeployed, its local disk state is discarded.

The application image itself is an immutable artifact stored by Porter.

## Examples

### React / Vite / static site
- Build output is immutable.
- Runtime can be **ephemeral**.
- No database/uploads needed unless the app adds them.

### Next.js
- App image is immutable.
- Runtime can be ephemeral.
- If it uses a database/object storage, that data must be persistent or external.

### Laravel / PHP
- App code is immutable.
- `storage/app`, uploads, SQLite, sessions need **persistent** storage unless moved to external DB/object storage.

### PostgreSQL / MySQL / Redis MicroVM
- Always **persistent**.
- Must survive stop/redeploy.
- Backups/snapshots required.

## Recommended fields

```text
persistence_mode: persistent | ephemeral
volume_size_mb
```

Default (as requested):

```text
persistent
```

Preview deployments should always be ephemeral.

## Runtime behavior

### Persistent
- creates/attaches a volume as `/dev/vdb`
- volume survives VM stop/delete
- redeploy reattaches the same volume
- snapshots/backups enabled

### Ephemeral
- no persistent volume
- rootfs/scratch deleted with the VM
- cheaper and safer for previews/stateless web apps

## Project defaults

```text
project.persistence_default = persistent
preview.persistence = ephemeral
```

The UI should explain the tradeoff in one line:

> "Ephemeral resets local disk on restart. Persistent keeps data across restarts and redeploys."
