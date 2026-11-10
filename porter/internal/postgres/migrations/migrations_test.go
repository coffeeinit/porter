package migrations

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestInitialMigrationIsVersionedAndForwardOnly(t *testing.T) {
	if InitialVersion != 1 {
		t.Fatalf("initial migration version = %d, want 1", InitialVersion)
	}
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded migration set is empty")
	}
	foundInitial := false
	for _, entry := range entries {
		if entry.Name() == "0001_core.sql" {
			foundInitial = true
		}
		if strings.HasSuffix(entry.Name(), ".down.sql") {
			t.Fatalf("forward-only migration set contains down migration %q", entry.Name())
		}
	}
	if !foundInitial {
		t.Fatalf("embedded migrations do not contain version %04d", InitialVersion)
	}
	if _, err := FS.ReadFile("0002_resource_events.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0002: %v", err)
	}
	if _, err := FS.ReadFile("0003_domains_routes.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0003: %v", err)
	}
	if _, err := FS.ReadFile("0004_storage_backup_metadata.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0004: %v", err)
	}
	if _, err := FS.ReadFile("0005_operations_command_leases.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0005: %v", err)
	}
	if _, err := FS.ReadFile("0006_agent_node_credentials.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0006: %v", err)
	}
	if _, err := FS.ReadFile("0007_agent_enrollment.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0007: %v", err)
	}
	if _, err := FS.ReadFile("0008_snapshot_restore_lifecycle.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0008: %v", err)
	}
	if _, err := FS.ReadFile("0009_release_readiness_health_gate.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0009: %v", err)
	}
	if _, err := FS.ReadFile("0010_agent_telemetry.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0010: %v", err)
	}
	if _, err := FS.ReadFile("0011_gitops_release_intent.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0011: %v", err)
	}
	if _, err := FS.ReadFile("0012_release_builder_receipts.sql"); err != nil {
		t.Fatalf("embedded migrations are missing version 0012: %v", err)
	}
	body, err := FS.ReadFile("0001_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 1.") {
		t.Fatal("migration header does not identify version 1")
	}
	if strings.Contains(sql, "\nBEGIN;\n") || strings.Contains(sql, "\nCOMMIT;\n") {
		t.Fatal("migration SQL must not manage transactions; the runner wraps SQL and ledger recording together")
	}
	if strings.Contains(strings.ToUpper(sql), "DROP TABLE") || strings.Contains(strings.ToUpper(sql), "DROP SCHEMA") {
		t.Fatal("forward-only initial migration must not contain destructive down operations")
	}
}

func TestInitialMigrationContainsOnlyApprovedCoreTables(t *testing.T) {
	body, err := FS.ReadFile("0001_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+porter_control_plane\.([a-z_]+)`)
	matches := re.FindAllStringSubmatch(string(body), -1)
	got := make([]string, 0, len(matches))
	for _, match := range matches {
		got = append(got, match[1])
	}
	sort.Strings(got)
	want := []string{"fleet_nodes", "instances", "projects", "releases", "services"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("created tables = %v, want %v", got, want)
	}
}

func TestInitialMigrationEnforcesRuntimeAndHierarchy(t *testing.T) {
	body, err := FS.ReadFile("0001_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CHECK (runtime = 'firecracker')",
		"UNIQUE (project_id, name)",
		"UNIQUE (service_id, sequence)",
		"UNIQUE (release_id, ordinal)",
		"FOREIGN KEY (release_id, service_id)",
		"WHERE state = 'active'",
		"artifact_digest ~ '^sha256:[0-9a-f]{64}$'",
		"resolved_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'",
		"CHECK (state NOT IN ('ready', 'deploying', 'active', 'superseded') OR resolved_commit IS NOT NULL)",
		"CREATE FUNCTION porter_control_plane.guard_release_intent()",
		"IS DISTINCT FROM",
		"CREATE TRIGGER releases_immutable_intent",
		"release artifact digest is immutable once set",
		"OLD.resolved_commit IS NOT NULL AND NEW.resolved_commit IS DISTINCT FROM OLD.resolved_commit",
		"release resolved commit is immutable once set",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration is missing invariant %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "CREATE EXTENSION") {
		t.Fatal("initial schema must not require database-wide extensions")
	}
}

func TestResourceEventsMigrationIsAppendOnlyAndIndexed(t *testing.T) {
	body, err := FS.ReadFile("0002_resource_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 2.") {
		t.Fatal("migration header does not identify version 2")
	}
	for _, required := range []string{
		"CREATE TABLE porter_control_plane.resource_events",
		"event_type TEXT NOT NULL",
		"occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"fleet_node_id TEXT",
		"data JSONB NOT NULL DEFAULT '{}'::jsonb",
		"resource_events_page_idx",
		"resource_events_append_only",
		"BEFORE UPDATE OR DELETE",
		"resource events are append-only",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("resource event migration is missing invariant %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "DROP TABLE") || strings.Contains(strings.ToUpper(sql), "DROP SCHEMA") ||
		strings.Contains(sql, "BEGIN;") || strings.Contains(sql, "COMMIT;") {
		t.Fatal("version 2 must be a forward-only migration body wrapped by the runner transaction")
	}
}

func TestDomainRoutesMigrationRequiresPersistedTXTProofAndStaysPreviewOnly(t *testing.T) {
	body, err := FS.ReadFile("0003_domains_routes.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 3.") {
		t.Fatal("migration header does not identify version 3")
	}
	for _, required := range []string{
		"CREATE TABLE porter_control_plane.domain_claims",
		"FOREIGN KEY (service_id, project_id)",
		"verification_txt_name",
		"verification_txt_value",
		"hostname !~ '^[0-9]+(\\.[0-9]+){3}$'",
		"ownership_proof_txt_value",
		"ownership_proved_at",
		"state = 'verified' AND ownership_proof_txt_value = verification_txt_value",
		"CREATE TABLE porter_control_plane.gateway_routes",
		"desired_enabled BOOLEAN NOT NULL DEFAULT FALSE",
		"path_prefix !~ '(^|/)([.]{1,2})(/|$)'",
		"CREATE FUNCTION porter_control_plane.require_verified_domain_for_route()",
		"TXT ownership proof is required before enabling route intent",
		"domain_claim_id TEXT",
		"gateway_route_id TEXT",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("domain/routes migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP TABLE", "DROP SCHEMA", "ACME", "CERTBOT", "CREATE PUBLICATION", "DNS PROVIDER"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("migration must not contain destructive, certificate, or provider-DNS operation %q", forbidden)
		}
	}
	if strings.Contains(sql, "BEGIN;") || strings.Contains(sql, "COMMIT;") {
		t.Fatal("version 3 must be wrapped in the explicit runner transaction")
	}
}

func TestStorageBackupMigrationIsScopedMetadataOnlyAndCrashConsistent(t *testing.T) {
	body, err := FS.ReadFile("0004_storage_backup_metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 4.") {
		t.Fatal("migration header does not identify version 4")
	}
	for _, required := range []string{
		"CREATE TABLE porter_control_plane.volume_metadata",
		"FOREIGN KEY (service_id, project_id)",
		"CHECK (access_mode = 'single_writer')",
		"state = 'metadata_only'",
		"node_affinity_id",
		"integrity_status",
		"UNIQUE (id, service_id)",
		"CREATE TABLE porter_control_plane.snapshot_metadata",
		"consistency = 'crash_consistent'",
		"capture_status = 'unavailable'",
		"FOREIGN KEY (instance_id, service_id)",
		"CREATE TABLE porter_control_plane.backup_metadata",
		"off_host_status = 'unavailable'",
		"FOREIGN KEY (snapshot_id, project_id, service_id, instance_id)",
		"volume_id TEXT",
		"snapshot_id TEXT",
		"backup_id TEXT",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("storage/backup migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP TABLE", "DROP SCHEMA", "SHARED_WRITABLE", "ATTACH DISK", "CREATE EXTENSION"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("storage/backup migration must not contain unsupported or destructive operation %q", forbidden)
		}
	}
	if strings.Contains(sql, "BEGIN;") || strings.Contains(sql, "COMMIT;") {
		t.Fatal("version 4 must be wrapped in the explicit runner transaction")
	}
}

func TestOperationsCommandLeaseMigrationIsScopedFencedAndAppendOnly(t *testing.T) {
	body, err := FS.ReadFile("0005_operations_command_leases.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 5.") {
		t.Fatal("migration header does not identify version 5")
	}
	for _, required := range []string{
		"CREATE TABLE porter_control_plane.operations",
		"FOREIGN KEY (service_id, project_id)",
		"FOREIGN KEY (instance_id, service_id)",
		"UNIQUE (project_id, idempotency_key)",
		"CREATE UNIQUE INDEX operations_one_active_per_instance_idx",
		"CREATE TABLE porter_control_plane.agent_commands",
		"'vm.apply', 'vm.delete', 'vm.start', 'vm.stop', 'vm.restart'",
		"payload_version SMALLINT NOT NULL DEFAULT 1 CHECK (payload_version = 1)",
		"lease_token_hash TEXT",
		"lease_expires_at TIMESTAMPTZ",
		"attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0)",
		"CREATE INDEX agent_commands_lease_expiry_idx",
		"CREATE TABLE porter_control_plane.agent_command_results",
		"UNIQUE (command_id, attempt, lease_token_hash)",
		"agent_command_results_append_only",
		"ADD COLUMN operation_id TEXT",
		"ADD COLUMN command_id TEXT",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("operations/command migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP TABLE", "DROP SCHEMA", "SHELL.EXEC", "CREATE EXTENSION"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("migration must not contain destructive, arbitrary-shell, or extension operation %q", forbidden)
		}
	}
	if strings.Contains(sql, "BEGIN;") || strings.Contains(sql, "COMMIT;") {
		t.Fatal("version 5 must be wrapped in the explicit runner transaction")
	}
}

func TestAgentNodeCredentialMigrationStoresOnlyHashesAndSupportsRotation(t *testing.T) {
	body, err := FS.ReadFile("0006_agent_node_credentials.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 6.") {
		t.Fatal("migration header does not identify version 6")
	}
	for _, required := range []string{
		"CREATE TABLE porter_control_plane.agent_node_credentials",
		"REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE CASCADE",
		"generation BIGINT NOT NULL CHECK (generation > 0)",
		"token_hash BYTEA NOT NULL CHECK (octet_length(token_hash) = 32)",
		"expires_at TIMESTAMPTZ",
		"revoked_at TIMESTAMPTZ",
		"agent_node_credentials_one_current_idx",
		"WHERE expires_at IS NULL AND revoked_at IS NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("agent credential migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"TOKEN TEXT", "BEARER_TOKEN", "DROP TABLE", "DROP SCHEMA", "CREATE EXTENSION"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("agent credential migration contains forbidden plaintext or destructive operation %q", forbidden)
		}
	}
	if strings.Contains(sql, "BEGIN;") || strings.Contains(sql, "COMMIT;") {
		t.Fatal("version 6 must be wrapped in the explicit runner transaction")
	}
}

func TestAgentEnrollmentMigrationIsHashedShortLivedSingleUseAndTopologyBound(t *testing.T) {
	body, err := FS.ReadFile("0007_agent_enrollment.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 7.") {
		t.Fatal("migration header does not identify version 7")
	}
	for _, required := range []string{
		"ADD COLUMN enrollment_target TEXT NOT NULL DEFAULT 'unspecified'",
		"CHECK (enrollment_target IN ('unspecified', 'same_host', 'remote_host'))",
		"CREATE TABLE porter_control_plane.agent_enrollment_tokens",
		"token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32)",
		"control_plane_url TEXT NOT NULL CHECK (control_plane_url ~ '^https://",
		"expires_at <= issued_at + interval '30 minutes'",
		"CHECK ((consumed_at IS NULL) = (fleet_node_id IS NULL))",
		"agent_enrollment_tokens_single_use",
		"agent enrollment tokens are retained for audit",
		"agent enrollment token may be consumed once only",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("agent enrollment migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"TOKEN TEXT", "BEARER_TOKEN", "DROP TABLE", "DROP SCHEMA", "CREATE EXTENSION", "SHELL.EXEC"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("agent enrollment migration contains forbidden plaintext, destructive, or arbitrary-command operation %q", forbidden)
		}
	}
	if strings.Contains(sql, "BEGIN;") || strings.Contains(sql, "COMMIT;") {
		t.Fatal("version 7 must be wrapped in the explicit runner transaction")
	}
}

func TestSnapshotRestoreMigrationIsDurableScopedAndPathFree(t *testing.T) {
	body, err := FS.ReadFile("0008_snapshot_restore_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 8.") {
		t.Fatal("migration header does not identify version 8")
	}
	for _, required := range []string{
		"ADD COLUMN runtime_vm_id TEXT",
		"capture_operation_id TEXT",
		"restore_status TEXT NOT NULL DEFAULT 'not_requested'",
		"restore_operation_id TEXT",
		"REFERENCES porter_control_plane.operations (id, project_id, service_id, instance_id)",
		"capture_status IN ('unavailable', 'queued', 'capturing', 'captured', 'failed')",
		"restore_status IN ('not_requested', 'queued', 'running', 'restored', 'failed')",
		"snapshot_artifact JSONB NOT NULL DEFAULT '{}'::jsonb",
		"capture_status = 'captured' AND artifact_sha256 IS NOT NULL",
		"integrity_status = 'verified'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("snapshot restore migration is missing invariant %q", required)
		}
	}
	for _, forbidden := range []string{"state_path TEXT", "mem_path TEXT", "socket_path TEXT", "firecracker_socket TEXT", "DROP TABLE", "DROP SCHEMA", "BEGIN;", "COMMIT;"} {
		if strings.Contains(strings.ToLower(sql), strings.ToLower(forbidden)) {
			t.Errorf("snapshot lifecycle migration contains path/socket/destructive transaction detail %q", forbidden)
		}
	}
}

func TestReleaseReadinessMigrationRequiresGuestReadyAndHealthGateForActivation(t *testing.T) {
	body, err := FS.ReadFile("0009_release_readiness_health_gate.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 9.") {
		t.Fatal("migration header does not identify version 9")
	}
	for _, required := range []string{
		"ADD COLUMN guest_ready_digest TEXT",
		"ADD COLUMN guest_ready_at TIMESTAMPTZ",
		"ADD COLUMN health_gate_status TEXT NOT NULL DEFAULT 'not_run'",
		"ADD COLUMN health_gate_digest TEXT",
		"guest_ready_digest = artifact_digest AND guest_ready_at IS NOT NULL",
		"health_gate_status = 'passed'",
		"CREATE FUNCTION porter_control_plane.guard_release_readiness()",
		"guest-ready artifact evidence is immutable once set",
		"completed release health-gate evidence is immutable",
		"release artifact must be guest-ready before ready, deploying, or superseded",
		"release must be guest-ready and pass its health gate before activation",
		"OLD.guest_ready_digest IS NULL",
		"OLD.health_gate_status <> 'passed'",
		"CREATE TRIGGER releases_readiness_guard",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("release readiness migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP TABLE", "DROP SCHEMA", "SHELL.EXEC", "CREATE EXTENSION", "BEGIN;", "COMMIT;"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("migration must remain forward-only and must not execute builds: %q", forbidden)
		}
	}
}

func TestTelemetryMigrationIsBoundedNodeOwnedAndRetained(t *testing.T) {
	body, err := FS.ReadFile("0010_agent_telemetry.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 10.") {
		t.Fatal("migration header does not identify version 10")
	}
	for _, required := range []string{
		"CREATE TABLE porter_control_plane.telemetry_logs",
		"CREATE TABLE porter_control_plane.telemetry_metrics",
		"REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE CASCADE",
		"REFERENCES porter_control_plane.instances(id) ON DELETE SET NULL",
		"payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32)",
		"octet_length(message) BETWEEN 1 AND 4096",
		"expires_at <= received_at + interval '30 days'",
		"telemetry_logs_node_time_idx",
		"telemetry_logs_instance_time_idx",
		"telemetry_metrics_node_time_idx",
		"telemetry_metrics_instance_time_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("telemetry migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP TABLE", "DROP SCHEMA", "CREATE EXTENSION", "BEGIN;", "COMMIT;"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("telemetry migration includes unsupported or destructive feature %q", forbidden)
		}
	}
}

func TestGitOpsReleaseIntentMigrationIsImmutableAndFailClosed(t *testing.T) {
	body, err := FS.ReadFile("0011_gitops_release_intent.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 11.") {
		t.Fatal("migration header does not identify version 11")
	}
	for _, required := range []string{
		"ADD COLUMN deployment_intent TEXT NOT NULL DEFAULT 'preview'",
		"CHECK (state IN ('queued', 'blocked', 'building', 'ready', 'deploying', 'active', 'failed', 'superseded'))",
		"deployment_intent)",
		"CREATE TABLE porter_control_plane.release_operations",
		"UNIQUE (service_id, idempotency_key)",
		"request_sha256",
		"state = 'blocked'",
		"CREATE TRIGGER release_operations_append_only",
		"CREATE FUNCTION porter_control_plane.guard_release_operation_append_only()",
		"does not assert that a build or deployment ran",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("GitOps migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP TABLE", "DROP SCHEMA", "SHELL.EXEC", "CREATE EXTENSION", "BEGIN;", "COMMIT;"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("GitOps migration includes unsupported or destructive behavior %q", forbidden)
		}
	}
}

func TestReleaseBuilderReceiptMigrationIsPinnedAppendOnlyAndNotGuestEvidence(t *testing.T) {
	body, err := FS.ReadFile("0012_release_builder_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.HasPrefix(strings.TrimSpace(sql), "-- Porter PostgreSQL foundation, version 12.") {
		t.Fatal("migration header does not identify version 12")
	}
	for _, required := range []string{
		"ADD COLUMN builder_spec JSONB",
		"state = 'running' AND kind = 'build'",
		"builder_spec->>'commit' = resolved_commit",
		"CREATE TABLE porter_control_plane.release_builder_results",
		"UNIQUE (operation_id, attempt)",
		"command_result->'builder_result'",
		"CREATE FUNCTION porter_control_plane.guard_release_builder_result()",
		"CREATE TRIGGER release_builder_results_match_pinned_operation",
		"release_builder_results_append_only",
		"BEFORE UPDATE OR DELETE",
		"release builder results are append-only",
		"An OCI result is not Firecracker guest-ready or production evidence",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("builder receipt migration is missing invariant %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP TABLE", "DROP SCHEMA", "SHELL.EXEC", "CREATE EXTENSION", "BEGIN;", "COMMIT;"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("builder receipt migration includes unsupported or destructive behavior %q", forbidden)
		}
	}
}
