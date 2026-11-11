# Results

**Target:** installer commit `31636e764adcb36c3580a4a16184eed58feea8df` (extracted read-only from Git objects). The source worktree `/workspace/porter-install-register` was at `eee9a993e90faa747de9632ed181be6a6e7fa69f` with pre-existing uncommitted changes; these results apply only to the reported SHA.

**Environment:** PostgreSQL 16.15. The registered cluster inventory before and after was unchanged; the existing `16/main` cluster remained `down` on port 5432. The disposable PG16 cluster used a private socket under `/tmp/porter-installer-pg16.*` and a random high port. The harness removed the cluster and temporary directory after the run; no disposable server process or directory remained.

## Commands and result

```bash
bash -n /workspace/porter-pg16-installer-acceptance-20261111/run.sh
# exit 0

/workspace/porter-pg16-installer-acceptance-20261111/run.sh
# exit 2 intentionally: partial acceptance because positive migration-before-service sequencing is blocked
```

The final run reported **15 executable checks passed, 1 acceptance item blocked**:

```text
PASS: exact source fails closed when Coder-owned porter-migrate.service is absent, before PG/systemd/package work
PASS: installer case fresh (exit=0; local root, private PG16 socket)
PASS: least-privilege porter role, porter-owned database, PUBLIC revocations, and schema grants verified on real PG16
PASS: installer case owned-existing (exit=0; local root, private PG16 socket)
PASS: pre-existing porter-owned non-template database accepted and normalized without createdb
PASS: installer case member-of-other (exit=1; private PG16 socket)
PASS: membership rejection verified when porter is the member
PASS: installer case other-member-of-porter (exit=1; private PG16 socket)
PASS: membership rejection verified when another role is a member of porter
PASS: installer case foreign-owner (exit=1; private PG16 socket)
PASS: unexpected foreign-owned porter database refused before role/database normalization
PASS: installer case template-database (exit=1; private PG16 socket)
PASS: unexpected template porter database refused without alteration
PASS: test mode does not activate Porter or run migrations; systemctl access stayed inside the stub
BLOCKED: positive migration-before-service sequencing; exact SHA lacks deploy/systemd/porter-migrate.service and deliberately reports no migration or Porter activation until Coder-owned unit/readers/migrator integration lands.
PASS: registered PostgreSQL cluster inventory unchanged; installer PG clients used only the disposable PG16 Unix socket
RESULT: 15 executable checks passed; 1 acceptance item(s) blocked.
ACCEPTANCE STATUS: PARTIAL/BLOCKED (do not treat as full install acceptance).
```

No real `apt-get` or host `systemctl` command was executed. Package-manager calls were blocked by the harness; systemd was stubbed. No migration ran and no Porter service was enabled or started. Positive migration-before-service order, clean-host installation, and real systemd behavior remain untested pending the backend-owned units/migrator/credential-reader integration.
