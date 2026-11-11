# Porter installer PG16 acceptance harness

This standalone acceptance artifact tests only installer commit `31636e764adcb36c3580a4a16184eed58feea8df` (parent `1f4bc65631ebe63cb82b3605c17cb79ca047b4e2`). It extracts that exact commit from Git objects. It does not test the final integrated beta branch.

## Run

From the repository root:

```bash
./tests/pg16-installer-acceptance/run.sh
```

The runner uses the current repository as its source by default. Set `PORTER_INSTALL_SOURCE_REPO` only when you need a different checkout containing the exact source commit.

`run.sh` requires PostgreSQL 16 server/client binaries, the local `postgres` OS account, and passwordless sudo. It creates a fresh cluster under a unique `/tmp/porter-installer-pg16.*` directory, binds only to a private Unix socket, runs the exact installer in `PORTER_INSTALL_TEST_MODE=1` with a temporary install root, and removes the cluster on exit. It snapshots `pg_lsclusters` before and after and fails if the registered cluster inventory changes.

The test uses a temporary root and command-interception seam. Installer `systemctl` calls go to a strict stub; only `systemctl --version` and the expected `postgresql.service` request are accepted. Package-manager calls are blocked. PG client commands use real PostgreSQL 16 binaries as the `postgres` OS user with a clean environment and the private socket/port explicitly set. No host cluster, apt operation, or real systemd command is used.

## Coverage and recorded result

The runner exercises the old candidate's local PostgreSQL checks and SQL against a real disposable PG16 cluster:

- Fresh `porter` role/database provisioning; least-privilege role flags, database ownership, removal of PUBLIC database privileges, `porter` CONNECT, and `public` schema USAGE/CREATE grants with PUBLIC CREATE revoked.
- Acceptance and normalization of an existing non-template `porter` database already owned by `porter`.
- Rejection of PostgreSQL role membership in both directions.
- Refusal of a foreign-owned `porter` database and of a template database, without normalizing rejected state.
- Fail-closed behavior when the exact source lacks the real `porter-migrate.service` unit.
- Test-mode non-activation and unchanged registered cluster inventory.

The captured run in [`RESULTS.md`](RESULTS.md) and [`RUN-OUTPUT.txt`](RUN-OUTPUT.txt) recorded 15 executable checks passed and one acceptance item blocked. The source was the exact candidate SHA above, not the final beta tip.

## Limits

The harness exits `2` intentionally because its pinned source commit lacks `deploy/systemd/porter-migrate.service` and does not pass the credential directives required by that harness. Synthetic unit fixtures only let it reach the database provisioning path; they are not evidence for production units or migration/service ordering. No real migration or Porter service action ran.

The beta branch now carries backend-owned migrator and service-unit changes, but this artifact does not test them. Migrator credential-file consumption, migration-before-service behavior, a clean Ubuntu/Debian installation, and real systemd acceptance remain pending. Do not use this candidate-only run as beta release evidence.
