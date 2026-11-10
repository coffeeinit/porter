package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "porter/controlplane/contract/v1"
	pgstore "porter/controlplane/internal/postgres/store"
)

const maxCursorLength = 2048

type cursorClaims struct {
	Version    int    `json:"v"`
	Resource   string `json:"resource"`
	Scope      string `json:"scope,omitempty"`
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at,omitempty"`
	OccurredAt string `json:"occurred_at,omitempty"`
	Sequence   int64  `json:"sequence,omitempty"`
}

func parsePageQuery(r *http.Request) (int, string, error) {
	query := r.URL.Query()
	for key := range query {
		if key != "page_size" && key != "cursor" {
			return 0, "", errors.New("unsupported list query parameter")
		}
	}
	pageSize := v1.DefaultPageSize
	if values, ok := query["page_size"]; ok {
		if len(values) != 1 {
			return 0, "", errors.New("page_size must appear once")
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 || n > v1.MaxPageSize {
			return 0, "", errors.New("page_size must be between 1 and 100")
		}
		pageSize = n
	}
	cursor := ""
	if values, ok := query["cursor"]; ok {
		if len(values) != 1 || values[0] == "" {
			return 0, "", errors.New("cursor must appear once and be non-empty")
		}
		cursor = values[0]
	}
	return pageSize, cursor, nil
}

func (s *Server) decodeTimeCursor(encoded, resource, scope string) (*pgstore.TimeCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	claims, err := s.decodeCursor(encoded, resource, scope)
	if err != nil || claims.CreatedAt == "" || claims.OccurredAt != "" || claims.Sequence != 0 {
		return nil, errors.New("invalid cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, claims.CreatedAt)
	if err != nil || createdAt.IsZero() || !validID(claims.ID) {
		return nil, errors.New("invalid cursor")
	}
	return &pgstore.TimeCursor{CreatedAt: createdAt, ID: claims.ID}, nil
}

func (s *Server) decodeReleaseCursor(encoded, resource, scope string) (*pgstore.ReleaseCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	claims, err := s.decodeCursor(encoded, resource, scope)
	if err != nil || claims.CreatedAt != "" || claims.OccurredAt != "" || claims.Sequence < 1 || !validID(claims.ID) {
		return nil, errors.New("invalid cursor")
	}
	return &pgstore.ReleaseCursor{Sequence: claims.Sequence, ID: claims.ID}, nil
}

func (s *Server) decodeEventCursor(encoded, resource, scope string) (*pgstore.EventCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	claims, err := s.decodeCursor(encoded, resource, scope)
	if err != nil || claims.CreatedAt != "" || claims.OccurredAt == "" || claims.Sequence != 0 {
		return nil, errors.New("invalid cursor")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, claims.OccurredAt)
	if err != nil || occurredAt.IsZero() || !validID(claims.ID) {
		return nil, errors.New("invalid cursor")
	}
	return &pgstore.EventCursor{OccurredAt: occurredAt, ID: claims.ID}, nil
}

func (s *Server) decodeCursor(encoded, resource, scope string) (cursorClaims, error) {
	if len(encoded) > maxCursorLength {
		return cursorClaims{}, errors.New("invalid cursor")
	}
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return cursorClaims{}, errors.New("invalid cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursorClaims{}, errors.New("invalid cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursorClaims{}, errors.New("invalid cursor")
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cursorClaims{}, errors.New("invalid cursor")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var claims cursorClaims
	if err := decoder.Decode(&claims); err != nil {
		return cursorClaims{}, errors.New("invalid cursor")
	}
	if claims.Version != 1 || claims.Resource != resource || claims.Scope != scope || !validID(claims.ID) {
		return cursorClaims{}, errors.New("invalid cursor")
	}
	return claims, nil
}

func (s *Server) encodeTimeCursor(resource, scope string, createdAt time.Time, id string) string {
	return s.encodeCursor(cursorClaims{
		Version: 1, Resource: resource, Scope: scope, ID: id,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) encodeReleaseCursor(resource, scope string, sequence int64, id string) string {
	return s.encodeCursor(cursorClaims{Version: 1, Resource: resource, Scope: scope, ID: id, Sequence: sequence})
}

func (s *Server) encodeEventCursor(resource, scope string, occurredAt time.Time, id string) string {
	return s.encodeCursor(cursorClaims{
		Version: 1, Resource: resource, Scope: scope, ID: id,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) encodeCursor(claims cursorClaims) string {
	payload, _ := json.Marshal(claims)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
