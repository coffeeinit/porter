-- Porter PostgreSQL foundation, version 3. Domain claims and preview-only route intent.
-- Applied explicitly by porter-migrate; this migration performs no provider DNS writes,
-- certificate automation, or gateway configuration.

-- The composite key makes project/service scope enforceable by foreign keys.
ALTER TABLE porter_control_plane.services
    ADD CONSTRAINT services_id_project_id_key UNIQUE (id, project_id);

CREATE TABLE porter_control_plane.domain_claims (
    id                       TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    project_id               TEXT NOT NULL,
    service_id               TEXT NOT NULL,
    hostname                 TEXT NOT NULL UNIQUE
                                 CHECK (length(hostname) BETWEEN 3 AND 253)
                                 CHECK (hostname = lower(hostname))
                                 CHECK (hostname ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$')
                                 CHECK (hostname !~ '^[0-9]+(\.[0-9]+){3}$'),
    state                    TEXT NOT NULL DEFAULT 'pending_proof'
                                 CHECK (state IN ('pending_proof', 'verified')),
    verification_txt_name    TEXT NOT NULL,
    verification_txt_value   TEXT NOT NULL
                                 CHECK (verification_txt_value ~ '^porter-verify=[0-9a-f]{64}$'),
    ownership_proof_txt_value TEXT,
    ownership_proved_at      TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, project_id, service_id),
    FOREIGN KEY (service_id, project_id)
        REFERENCES porter_control_plane.services (id, project_id) ON DELETE RESTRICT,
    CHECK (verification_txt_name = '_porter-verify.' || hostname),
    CHECK (
        (state = 'pending_proof' AND ownership_proof_txt_value IS NULL AND ownership_proved_at IS NULL)
        OR
        (state = 'verified' AND ownership_proof_txt_value = verification_txt_value AND ownership_proved_at IS NOT NULL)
    )
);

CREATE INDEX domain_claims_project_page_idx
    ON porter_control_plane.domain_claims (project_id, created_at, id);

CREATE INDEX domain_claims_service_page_idx
    ON porter_control_plane.domain_claims (service_id, created_at, id);

CREATE FUNCTION porter_control_plane.guard_domain_claim_proof()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(NEW.id, NEW.project_id, NEW.service_id, NEW.hostname,
           NEW.verification_txt_name, NEW.verification_txt_value, NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.id, OLD.project_id, OLD.service_id, OLD.hostname,
           OLD.verification_txt_name, OLD.verification_txt_value, OLD.created_at) THEN
        RAISE EXCEPTION 'domain claim identity and TXT challenge are immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    IF OLD.state = 'verified'
       AND ROW(NEW.state, NEW.ownership_proof_txt_value, NEW.ownership_proved_at)
           IS DISTINCT FROM ROW(OLD.state, OLD.ownership_proof_txt_value, OLD.ownership_proved_at) THEN
        RAISE EXCEPTION 'persisted domain ownership proof is immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER domain_claims_proof_immutable
    BEFORE UPDATE ON porter_control_plane.domain_claims
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.guard_domain_claim_proof();

CREATE TABLE porter_control_plane.gateway_routes (
    id              TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    domain_claim_id TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    service_id      TEXT NOT NULL,
    path_prefix     TEXT NOT NULL DEFAULT '/'
                        CHECK (length(path_prefix) BETWEEN 1 AND 256)
                        CHECK (path_prefix <> '//')
                        CHECK (path_prefix !~ '(^|/)([.]{1,2})(/|$)')
                        CHECK (path_prefix ~ '^/([A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*)?/?$'),
    desired_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_claim_id, path_prefix),
    FOREIGN KEY (domain_claim_id, project_id, service_id)
        REFERENCES porter_control_plane.domain_claims (id, project_id, service_id) ON DELETE RESTRICT
);

CREATE INDEX gateway_routes_claim_page_idx
    ON porter_control_plane.gateway_routes (domain_claim_id, created_at, id);

CREATE FUNCTION porter_control_plane.require_verified_domain_for_route()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    claim_state TEXT;
BEGIN
    SELECT state INTO claim_state
      FROM porter_control_plane.domain_claims
     WHERE id = NEW.domain_claim_id
     FOR UPDATE;

    IF NEW.desired_enabled AND claim_state IS DISTINCT FROM 'verified' THEN
        RAISE EXCEPTION 'TXT ownership proof is required before enabling route intent'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER gateway_routes_require_verified_domain
    BEFORE INSERT OR UPDATE OF desired_enabled, domain_claim_id
    ON porter_control_plane.gateway_routes
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.require_verified_domain_for_route();

-- Events remain append-only; optional references retain history without FKs.
ALTER TABLE porter_control_plane.resource_events
    ADD COLUMN domain_claim_id TEXT,
    ADD COLUMN gateway_route_id TEXT;

CREATE INDEX resource_events_domain_claim_page_idx
    ON porter_control_plane.resource_events (domain_claim_id, occurred_at DESC, id DESC)
    WHERE domain_claim_id IS NOT NULL;

CREATE INDEX resource_events_gateway_route_page_idx
    ON porter_control_plane.resource_events (gateway_route_id, occurred_at DESC, id DESC)
    WHERE gateway_route_id IS NOT NULL;
