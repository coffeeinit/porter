package store

import (
	"context"
	"fmt"

	v1 "porter/controlplane/contract/v1"
)

func (s *Store) ListDomainClaimsPage(ctx context.Context, projectID, serviceID string, limit int, after *TimeCursor) ([]v1.DomainClaim, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTimeCursor(after); err != nil {
		return nil, err
	}
	var createdAt, id any
	if after != nil {
		createdAt, id = after.CreatedAt, after.ID
	}
	rows, err := s.db.QueryContext(ctx, domainClaimSelect+`
WHERE ($1::text IS NULL OR project_id = $1)
  AND ($2::text IS NULL OR service_id = $2)
  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3::timestamptz, $4::text))
ORDER BY created_at, id
LIMIT $5`, nullableID(projectID), nullableID(serviceID), createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list domain claim page: %w", err)
	}
	return scanRows(rows, "domain claim", scanDomainClaim)
}

func (s *Store) ListGatewayRoutesPage(ctx context.Context, claimID string, limit int, after *TimeCursor) ([]v1.GatewayRoute, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTimeCursor(after); err != nil {
		return nil, err
	}
	var createdAt, id any
	if after != nil {
		createdAt, id = after.CreatedAt, after.ID
	}
	rows, err := s.db.QueryContext(ctx, gatewayRouteSelect+`
WHERE r.domain_claim_id = $1
  AND ($2::timestamptz IS NULL OR (r.created_at, r.id) > ($2::timestamptz, $3::text))
ORDER BY r.created_at, r.id
LIMIT $4`, claimID, createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list gateway route page: %w", err)
	}
	return scanRows(rows, "gateway route", scanGatewayRoute)
}
