package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/agentapi"
	"porter/controlplane/internal/postgres/httpapi"
	pgstore "porter/controlplane/internal/postgres/store"
	"porter/controlplane/web"
)

const postgresPanelContractMarker = `<meta name="porter-api-contract" content="postgres-only">`

const postgresPanelUnavailableHTML = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Porter panel unavailable</title><body><main><h1>Porter panel unavailable</h1><p>The embedded UI bundle is not verified as PostgreSQL-only. It has been withheld to avoid demo data or retired API calls.</p><p>Install the safe Vue bundle at <code>porter/web/index.html</code>; <code>porter/web/web.go</code> embeds that file.</p></main></body></html>`

// configureAPIRoutes builds both API adapters over one explicitly configured
// PostgreSQL connection. It never opens SQLite, falls back to legacy routes, or
// applies migrations during startup. Errors are generic and never include DSN data.
func configureAPIRoutes(adminToken, dsn string) (http.Handler, func(), error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, nil, errors.New("PostgreSQL DSN credential is required")
	}
	if strings.TrimSpace(adminToken) == "" {
		return nil, nil, errors.New("operator admin credential is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, errors.New("open PostgreSQL API database failed")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	closeDB := func() { _ = db.Close() }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		closeDB()
		return nil, nil, errors.New("connect to PostgreSQL API database failed")
	}
	if err := verifyPostgresAPISchema(ctx, db); err != nil {
		closeDB()
		return nil, nil, err
	}
	repository, err := pgstore.New(db)
	if err != nil {
		closeDB()
		return nil, nil, errors.New("initialize PostgreSQL API store failed")
	}
	controlPlane, err := httpapi.New(repository, adminToken)
	if err != nil {
		closeDB()
		return nil, nil, errors.New("initialize PostgreSQL API failed")
	}
	agents, err := agentapi.New(repository, adminToken)
	if err != nil {
		closeDB()
		return nil, nil, errors.New("initialize PostgreSQL agent API failed")
	}
	return postgresOnlyMux(controlPlane, agents), closeDB, nil
}

type fleetPublicRoutes struct {
	joinTokenIssue       http.Handler
	agentEnrollment      http.Handler
	nodeList             http.Handler
	nodeCredentialRotate http.Handler
}

// mountFleetRoutes derives capability flags from the handlers it actually
// registers. Missing handlers are not represented as available capabilities.
func mountFleetRoutes(mux *http.ServeMux, routes fleetPublicRoutes) v1.FleetCapabilities {
	var capabilities v1.FleetCapabilities
	if routes.joinTokenIssue != nil {
		mux.Handle("/api/v1/fleet/join-tokens", routes.joinTokenIssue)
		capabilities.JoinTokenIssuance = true
	}
	if routes.agentEnrollment != nil {
		mux.Handle("/api/v1/fleet/enroll", routes.agentEnrollment)
		capabilities.AgentEnrollment = true
	}
	if routes.nodeList != nil {
		mux.Handle("/api/v1/fleet/nodes", routes.nodeList)
	}
	if routes.nodeCredentialRotate != nil {
		mux.Handle("/api/v1/fleet/nodes/", routes.nodeCredentialRotate)
		capabilities.NodeCredentialRotation = true
	}
	return capabilities
}

// postgresOnlyMux mounts the frozen public Fleet aliases over the PostgreSQL
// adapters and never delegates to a SQLite handler.
func postgresOnlyMux(controlPlane, agents http.Handler) http.Handler {
	mux := http.NewServeMux()
	routes := fleetPublicRoutes{
		joinTokenIssue:       rewriteAgentPath(agents, "/internal/admin/fleet/enrollment-tokens"),
		agentEnrollment:      rewriteAgentPath(agents, "/internal/agent/enroll"),
		nodeList:             controlPlane,
		nodeCredentialRotate: rewriteAgentPathPrefix(agents, "/api/v1/fleet/nodes/", "/internal/admin/fleet/nodes/"),
	}
	capabilities := mountFleetRoutes(mux, routes)
	if controlPlane != nil {
		mux.Handle("/api/v1", controlPlane)
		mux.Handle("/api/v1/", controlPlane)
	}
	if provider, ok := agents.(interface {
		FleetCapabilitiesHandler(v1.FleetCapabilities) http.Handler
	}); ok {
		capabilityHandler := provider.FleetCapabilitiesHandler(capabilities)
		if capabilityHandler != nil {
			mux.Handle("/api/v1/fleet/capabilities", capabilityHandler)
		} else {
			mux.Handle("/api/v1/fleet/capabilities", http.NotFoundHandler())
		}
	} else {
		mux.Handle("/api/v1/fleet/capabilities", http.NotFoundHandler())
	}
	if agents != nil {
		for _, route := range []string{
			"/internal/agent/heartbeat",
			"/internal/agent/telemetry",
			"/internal/agent/commands",
			"/internal/agent/results",
		} {
			mux.Handle(route, agents)
		}
	}
	mux.Handle("/internal/agent/enroll", retiredFleetHandler("use POST /api/v1/fleet/enroll"))
	mux.Handle("/v1", retiredFleetHandler("legacy API routes are retired in PostgreSQL-only mode"))
	mux.Handle("/v1/", retiredFleetHandler("legacy API routes are retired in PostgreSQL-only mode"))
	mux.Handle("/", postgresPanelHandler(web.Index))
	return mux
}

func rewriteAgentPath(agents http.Handler, internalPath string) http.Handler {
	if agents == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		updatedURL := *r.URL
		updatedURL.Path = internalPath
		updatedURL.RawPath = ""
		clone.URL = &updatedURL
		agents.ServeHTTP(w, clone)
	})
}

func rewriteAgentPathPrefix(agents http.Handler, externalPrefix, internalPrefix string) http.Handler {
	if agents == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		updatedURL := *r.URL
		updatedURL.Path = internalPrefix + strings.TrimPrefix(r.URL.Path, externalPrefix)
		updatedURL.RawPath = ""
		clone.URL = &updatedURL
		agents.ServeHTTP(w, clone)
	})
}

func postgresPanelHandler(index []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if !isPostgreSQLPanelBundle(index) {
			w.WriteHeader(http.StatusServiceUnavailable)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(postgresPanelUnavailableHTML))
			}
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	})
}

func isPostgreSQLPanelBundle(index []byte) bool {
	for _, required := range []string{
		postgresPanelContractMarker,
		`type="password"`,
		"/api/v1/fleet/capabilities",
		"/api/v1/fleet/join-tokens",
		"/api/v1/fleet/enroll",
		"/api/v1/fleet/nodes",
	} {
		if !bytes.Contains(index, []byte(required)) {
			return false
		}
	}
	for _, legacy := range []string{
		"const sample=", "Demo preview", "SQLite connected",
		"localStorage", "sessionStorage", "location.search", "console.log",
		"api('/v1/", `api("/v1/`, "api(`/v1/", "fetch('/v1/", `fetch("/v1/`, "fetch(`/v1/",
	} {
		if bytes.Contains(index, []byte(legacy)) {
			return false
		}
	}
	return true
}

func retiredFleetHandler(message string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var randomID [8]byte
		requestID := "req_unavailable"
		if _, err := rand.Read(randomID[:]); err == nil {
			requestID = "req_" + hex.EncodeToString(randomID[:])
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Request-ID", requestID)
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(v1.ErrorResponse{Error: v1.ErrorDetail{
			Code: v1.ErrorFailedPrecondition, Message: message, RequestID: requestID,
		}})
	})
}

func verifyPostgresAPISchema(ctx context.Context, db *sql.DB) error {
	var ready bool
	err := db.QueryRowContext(ctx, `
	SELECT to_regclass('public.porter_control_plane_schema_migrations') IS NOT NULL
	   AND to_regclass('porter_control_plane.projects') IS NOT NULL
	   AND to_regclass('porter_control_plane.services') IS NOT NULL
	   AND to_regclass('porter_control_plane.releases') IS NOT NULL
	   AND to_regclass('porter_control_plane.instances') IS NOT NULL
	   AND to_regclass('porter_control_plane.fleet_nodes') IS NOT NULL
	   AND to_regclass('porter_control_plane.resource_events') IS NOT NULL
	   AND to_regclass('porter_control_plane.domain_claims') IS NOT NULL
	   AND to_regclass('porter_control_plane.gateway_routes') IS NOT NULL
	   AND to_regclass('porter_control_plane.volume_metadata') IS NOT NULL
	   AND to_regclass('porter_control_plane.snapshot_metadata') IS NOT NULL
	   AND to_regclass('porter_control_plane.backup_metadata') IS NOT NULL
	   AND to_regclass('porter_control_plane.operations') IS NOT NULL
	   AND to_regclass('porter_control_plane.agent_commands') IS NOT NULL
	   AND to_regclass('porter_control_plane.agent_command_results') IS NOT NULL
	   AND to_regclass('porter_control_plane.agent_enrollment_tokens') IS NOT NULL
	   AND to_regclass('porter_control_plane.agent_node_credentials') IS NOT NULL
	   AND to_regclass('porter_control_plane.telemetry_logs') IS NOT NULL
	   AND to_regclass('porter_control_plane.telemetry_metrics') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return errors.New("verify PostgreSQL API schema failed")
	}
	if !ready {
		return errors.New("PostgreSQL API schema is not initialized; apply migrations with porter-migrate.service first")
	}
	var applied bool
	for _, migration := range []struct {
		version int
		name    string
	}{
		{5, "0005_operations_command_leases.sql"},
		{7, "0007_agent_enrollment.sql"},
		{8, "0008_snapshot_restore_lifecycle.sql"},
		{9, "0009_release_readiness_health_gate.sql"},
		{10, "0010_agent_telemetry.sql"},
		{11, "0011_gitops_release_intent.sql"},
		{12, "0012_release_builder_receipts.sql"},
	} {
		err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM public.porter_control_plane_schema_migrations
		    WHERE version = $1 AND name = $2
		)`, migration.version, migration.name).Scan(&applied)
		if err != nil {
			return errors.New("verify PostgreSQL API migration history failed")
		}
		if !applied {
			return fmt.Errorf("PostgreSQL API requires migration %s; apply migrations with porter-migrate.service first", migration.name)
		}
	}
	return nil
}
