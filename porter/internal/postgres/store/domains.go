package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

const domainClaimSelect = `
SELECT id, project_id, service_id, hostname, state, verification_txt_name,
       verification_txt_value, ownership_proof_txt_value, ownership_proved_at,
       created_at, updated_at
FROM porter_control_plane.domain_claims`

const gatewayRouteSelect = `
SELECT r.id, r.domain_claim_id, r.project_id, r.service_id, c.hostname,
       r.path_prefix, r.desired_enabled, r.created_at, r.updated_at
FROM porter_control_plane.gateway_routes r
JOIN porter_control_plane.domain_claims c ON c.id = r.domain_claim_id`

func normalizeHostname(input string) (string, error) {
	hostname := strings.ToLower(strings.TrimSpace(input))
	if strings.HasSuffix(hostname, ".") {
		hostname = strings.TrimSuffix(hostname, ".")
	}
	if len(hostname) < 3 || len(hostname) > 253 || !strings.Contains(hostname, ".") || net.ParseIP(hostname) != nil || strings.ContainsAny(hostname, ":/%\\*?@# \t\r\n") {
		return "", fmt.Errorf("%w: invalid DNS hostname", ErrInvalidArgument)
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) < 1 || len(label) > 63 || !isASCIIAlphaNumeric(label[0]) || !isASCIIAlphaNumeric(label[len(label)-1]) {
			return "", fmt.Errorf("%w: invalid DNS hostname", ErrInvalidArgument)
		}
		for i := 0; i < len(label); i++ {
			if !isASCIIAlphaNumeric(label[i]) && label[i] != '-' {
				return "", fmt.Errorf("%w: invalid DNS hostname", ErrInvalidArgument)
			}
		}
	}
	return hostname, nil
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func validateRoutePathPrefix(value string) error {
	if value == "" {
		value = "/"
	}
	if len(value) > 256 || value[0] != '/' || strings.ContainsAny(value, "?#\\%\x00\r\n\t") {
		return fmt.Errorf("%w: invalid route path prefix", ErrInvalidArgument)
	}
	if value != "/" {
		trimmed := strings.TrimSuffix(value, "/")
		if trimmed == "" || strings.Contains(trimmed, "//") {
			return fmt.Errorf("%w: invalid route path prefix", ErrInvalidArgument)
		}
		for _, segment := range strings.Split(strings.TrimPrefix(trimmed, "/"), "/") {
			if segment == "" || segment == "." || segment == ".." {
				return fmt.Errorf("%w: invalid route path prefix", ErrInvalidArgument)
			}
			for i := 0; i < len(segment); i++ {
				c := segment[i]
				if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '.' && c != '_' && c != '~' && c != '-' {
					return fmt.Errorf("%w: invalid route path prefix", ErrInvalidArgument)
				}
			}
		}
	}
	return nil
}

func newTXTVerificationValue() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate domain TXT challenge: %w", err)
	}
	return "porter-verify=" + hex.EncodeToString(raw[:]), nil
}

func (s *Store) CreateDomainClaim(ctx context.Context, projectID, serviceID, hostname string) (v1.DomainClaim, error) {
	hostname, err := normalizeHostname(hostname)
	if err != nil {
		return v1.DomainClaim{}, err
	}
	id, err := newID("dcl")
	if err != nil {
		return v1.DomainClaim{}, err
	}
	txtValue, err := newTXTVerificationValue()
	if err != nil {
		return v1.DomainClaim{}, err
	}
	txtName := "_porter-verify." + hostname

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.DomainClaim{}, fmt.Errorf("begin domain claim creation: %w", err)
	}
	defer tx.Rollback()
	var projectState v1.ProjectState
	var serviceState v1.ServiceState
	err = tx.QueryRowContext(ctx, `
SELECT p.state, s.state
FROM porter_control_plane.projects p
JOIN porter_control_plane.services s ON s.project_id = p.id
WHERE p.id = $1 AND s.id = $2
FOR UPDATE OF p, s`, projectID, serviceID).Scan(&projectState, &serviceState)
	if err != nil {
		if err == sql.ErrNoRows {
			return v1.DomainClaim{}, fmt.Errorf("%w: service is not in project", ErrNotFound)
		}
		return v1.DomainClaim{}, fmt.Errorf("lock domain claim project/service: %w", err)
	}
	if projectState != v1.ProjectActive || serviceState != v1.ServiceActive {
		return v1.DomainClaim{}, fmt.Errorf("%w: domain claims require an active project and service", ErrFailedPrecondition)
	}

	claim, err := scanDomainClaim(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.domain_claims
    (id, project_id, service_id, hostname, verification_txt_name, verification_txt_value)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, project_id, service_id, hostname, state, verification_txt_name,
          verification_txt_value, ownership_proof_txt_value, ownership_proved_at,
          created_at, updated_at`, id, projectID, serviceID, hostname, txtName, txtValue))
	if err != nil {
		return v1.DomainClaim{}, fmt.Errorf("create domain claim: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "domain_claim.created", eventRefs{
		projectID: projectID, serviceID: serviceID, domainClaimID: claim.ID,
	}, map[string]any{"hostname": claim.Hostname, "state": claim.State}); err != nil {
		return v1.DomainClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.DomainClaim{}, fmt.Errorf("commit domain claim creation: %w", err)
	}
	return claim, nil
}

func (s *Store) GetDomainClaim(ctx context.Context, id string) (v1.DomainClaim, error) {
	claim, err := scanDomainClaim(s.db.QueryRowContext(ctx, domainClaimSelect+` WHERE id = $1`, id))
	if err != nil {
		return v1.DomainClaim{}, notFound("domain claim", id, err)
	}
	return claim, nil
}

func (s *Store) RecordDomainTXTProof(ctx context.Context, id, observedValue string) (v1.DomainClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.DomainClaim{}, fmt.Errorf("begin domain proof update: %w", err)
	}
	defer tx.Rollback()
	claim, err := scanDomainClaim(tx.QueryRowContext(ctx, domainClaimSelect+` WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return v1.DomainClaim{}, notFound("domain claim", id, err)
	}
	if !hmac.Equal([]byte(claim.VerificationTXTValue), []byte(observedValue)) {
		return v1.DomainClaim{}, fmt.Errorf("%w: TXT value does not match challenge", ErrFailedPrecondition)
	}
	if claim.State == v1.DomainClaimVerified {
		return claim, nil
	}
	if err := tx.QueryRowContext(ctx, `
UPDATE porter_control_plane.domain_claims
SET state = 'verified', ownership_proof_txt_value = $2,
    ownership_proved_at = now(), updated_at = now()
WHERE id = $1
RETURNING ownership_proof_txt_value, ownership_proved_at, updated_at`, id, observedValue).
		Scan(&claim.OwnershipProofTXTValue, &claim.OwnershipProvedAt, &claim.UpdatedAt); err != nil {
		return v1.DomainClaim{}, fmt.Errorf("persist domain TXT proof: %w", err)
	}
	claim.State = v1.DomainClaimVerified
	if err := s.appendEvent(ctx, tx, "domain_claim.verified", eventRefs{
		projectID: claim.ProjectID, serviceID: claim.ServiceID, domainClaimID: claim.ID,
	}, map[string]any{"hostname": claim.Hostname, "proof": "txt"}); err != nil {
		return v1.DomainClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.DomainClaim{}, fmt.Errorf("commit domain TXT proof: %w", err)
	}
	return claim, nil
}

func (s *Store) CreateGatewayRoute(ctx context.Context, claimID, pathPrefix string) (v1.GatewayRoute, error) {
	if pathPrefix == "" {
		pathPrefix = "/"
	}
	if err := validateRoutePathPrefix(pathPrefix); err != nil {
		return v1.GatewayRoute{}, err
	}
	id, err := newID("grt")
	if err != nil {
		return v1.GatewayRoute{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.GatewayRoute{}, fmt.Errorf("begin gateway route creation: %w", err)
	}
	defer tx.Rollback()
	claim, err := scanDomainClaim(tx.QueryRowContext(ctx, domainClaimSelect+` WHERE id = $1 FOR UPDATE`, claimID))
	if err != nil {
		return v1.GatewayRoute{}, notFound("domain claim", claimID, err)
	}
	var route v1.GatewayRoute
	err = tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.gateway_routes
    (id, domain_claim_id, project_id, service_id, path_prefix)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, domain_claim_id, project_id, service_id, path_prefix,
          desired_enabled, created_at, updated_at`,
		id, claim.ID, claim.ProjectID, claim.ServiceID, pathPrefix).
		Scan(&route.ID, &route.DomainClaimID, &route.ProjectID, &route.ServiceID,
			&route.PathPrefix, &route.DesiredEnabled, &route.CreatedAt, &route.UpdatedAt)
	if err != nil {
		return v1.GatewayRoute{}, fmt.Errorf("create gateway route intent: %w", err)
	}
	route.Hostname = claim.Hostname
	route.GatewayStatus = v1.GatewayUnavailable
	if err := s.appendEvent(ctx, tx, "gateway_route.created", eventRefs{
		projectID: route.ProjectID, serviceID: route.ServiceID,
		domainClaimID: route.DomainClaimID, gatewayRouteID: route.ID,
	}, map[string]any{"hostname": route.Hostname, "path_prefix": route.PathPrefix, "desired_enabled": false}); err != nil {
		return v1.GatewayRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.GatewayRoute{}, fmt.Errorf("commit gateway route creation: %w", err)
	}
	return route, nil
}

func (s *Store) GetGatewayRoute(ctx context.Context, id string) (v1.GatewayRoute, error) {
	route, err := scanGatewayRoute(s.db.QueryRowContext(ctx, gatewayRouteSelect+` WHERE r.id = $1`, id))
	if err != nil {
		return v1.GatewayRoute{}, notFound("gateway route", id, err)
	}
	return route, nil
}

func (s *Store) SetGatewayRouteDesiredEnabled(ctx context.Context, id string, desiredEnabled bool) (v1.GatewayRoute, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.GatewayRoute{}, fmt.Errorf("begin gateway route update: %w", err)
	}
	defer tx.Rollback()
	var route v1.GatewayRoute
	var claimState v1.DomainClaimState
	err = tx.QueryRowContext(ctx, `
SELECT r.id, r.domain_claim_id, r.project_id, r.service_id, c.hostname,
       c.state, r.path_prefix, r.desired_enabled, r.created_at, r.updated_at
FROM porter_control_plane.gateway_routes r
JOIN porter_control_plane.domain_claims c ON c.id = r.domain_claim_id
WHERE r.id = $1
FOR UPDATE OF r, c`, id).Scan(&route.ID, &route.DomainClaimID, &route.ProjectID, &route.ServiceID,
		&route.Hostname, &claimState, &route.PathPrefix, &route.DesiredEnabled, &route.CreatedAt, &route.UpdatedAt)
	if err != nil {
		return v1.GatewayRoute{}, notFound("gateway route", id, err)
	}
	if desiredEnabled && claimState != v1.DomainClaimVerified {
		return v1.GatewayRoute{}, fmt.Errorf("%w: a persisted TXT ownership proof is required before enabling route intent", ErrFailedPrecondition)
	}
	if route.DesiredEnabled == desiredEnabled {
		route.GatewayStatus = v1.GatewayUnavailable
		if err := tx.Commit(); err != nil {
			return v1.GatewayRoute{}, fmt.Errorf("commit unchanged gateway route: %w", err)
		}
		return route, nil
	}
	if err := tx.QueryRowContext(ctx, `
UPDATE porter_control_plane.gateway_routes
SET desired_enabled = $2, updated_at = now()
WHERE id = $1
RETURNING updated_at`, id, desiredEnabled).Scan(&route.UpdatedAt); err != nil {
		return v1.GatewayRoute{}, fmt.Errorf("update gateway route intent: %w", err)
	}
	previous := route.DesiredEnabled
	route.DesiredEnabled = desiredEnabled
	route.GatewayStatus = v1.GatewayUnavailable
	if err := s.appendEvent(ctx, tx, "gateway_route.desired_state_changed", eventRefs{
		projectID: route.ProjectID, serviceID: route.ServiceID,
		domainClaimID: route.DomainClaimID, gatewayRouteID: route.ID,
	}, map[string]any{"from": previous, "to": desiredEnabled}); err != nil {
		return v1.GatewayRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.GatewayRoute{}, fmt.Errorf("commit gateway route intent: %w", err)
	}
	return route, nil
}

func scanDomainClaim(row rowScanner) (v1.DomainClaim, error) {
	var claim v1.DomainClaim
	var proofValue sql.NullString
	var provedAt sql.NullTime
	err := row.Scan(&claim.ID, &claim.ProjectID, &claim.ServiceID, &claim.Hostname, &claim.State,
		&claim.VerificationTXTName, &claim.VerificationTXTValue, &proofValue, &provedAt,
		&claim.CreatedAt, &claim.UpdatedAt)
	if err != nil {
		return v1.DomainClaim{}, err
	}
	if proofValue.Valid {
		claim.OwnershipProofTXTValue = proofValue.String
	}
	if provedAt.Valid {
		value := provedAt.Time
		claim.OwnershipProvedAt = &value
	}
	return claim, nil
}

func scanGatewayRoute(row rowScanner) (v1.GatewayRoute, error) {
	var route v1.GatewayRoute
	err := row.Scan(&route.ID, &route.DomainClaimID, &route.ProjectID, &route.ServiceID,
		&route.Hostname, &route.PathPrefix, &route.DesiredEnabled, &route.CreatedAt, &route.UpdatedAt)
	if err == nil {
		route.GatewayStatus = v1.GatewayUnavailable
	}
	return route, err
}
