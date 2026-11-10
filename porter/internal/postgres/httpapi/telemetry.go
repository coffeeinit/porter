package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "porter/controlplane/contract/v1"
	pgstore "porter/controlplane/internal/postgres/store"
)

const maxTelemetryQueryRange = 30 * 24 * time.Hour

func (s *Server) handleTelemetryLogs(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	pageSize, cursorText, filter, scope, ok := parseTelemetryQuery(w, r)
	if !ok {
		return
	}
	cursor, err := s.decodeEventCursor(cursorText, "telemetry-logs", scope)
	if err != nil {
		badPageQuery(w, "invalid telemetry page cursor")
		return
	}
	items, err := s.repository.ListTelemetryLogsPage(r.Context(), pageSize+1, filter, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.TelemetryLog]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeEventCursor("telemetry-logs", scope, last.ReportedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleTelemetryMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	pageSize, cursorText, filter, scope, ok := parseTelemetryQuery(w, r)
	if !ok {
		return
	}
	cursor, err := s.decodeEventCursor(cursorText, "telemetry-metrics", scope)
	if err != nil {
		badPageQuery(w, "invalid telemetry page cursor")
		return
	}
	items, err := s.repository.ListTelemetryMetricsPage(r.Context(), pageSize+1, filter, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.TelemetryMetric]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeEventCursor("telemetry-metrics", scope, last.ReportedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func parseTelemetryQuery(w http.ResponseWriter, r *http.Request) (int, string, pgstore.TelemetryFilter, string, bool) {
	query := r.URL.Query()
	allowed := map[string]bool{"from": true, "to": true, "node_id": true, "instance_id": true, "page_size": true, "cursor": true}
	for key := range query {
		if !allowed[key] {
			badPageQuery(w, "unsupported telemetry query parameter")
			return 0, "", pgstore.TelemetryFilter{}, "", false
		}
	}
	one := func(key string) (string, bool) {
		values, exists := query[key]
		if !exists {
			return "", true
		}
		if len(values) != 1 {
			return "", false
		}
		return values[0], true
	}
	pageSize := v1.DefaultPageSize
	pageText, ok := one("page_size")
	if !ok {
		badPageQuery(w, "page_size must appear once")
		return 0, "", pgstore.TelemetryFilter{}, "", false
	}
	if pageText != "" {
		n, err := strconv.Atoi(pageText)
		if err != nil || n < 1 || n > v1.MaxPageSize {
			badPageQuery(w, "page_size must be between 1 and 100")
			return 0, "", pgstore.TelemetryFilter{}, "", false
		}
		pageSize = n
	}
	cursorText, ok := one("cursor")
	if !ok || len(cursorText) > maxCursorLength || (query.Has("cursor") && cursorText == "") {
		badPageQuery(w, "cursor must appear once and be non-empty")
		return 0, "", pgstore.TelemetryFilter{}, "", false
	}
	fromText, ok := one("from")
	if !ok {
		badPageQuery(w, "from must appear once")
		return 0, "", pgstore.TelemetryFilter{}, "", false
	}
	toText, ok := one("to")
	if !ok {
		badPageQuery(w, "to must appear once")
		return 0, "", pgstore.TelemetryFilter{}, "", false
	}
	nodeID, ok := one("node_id")
	if !ok || (nodeID != "" && !validID(nodeID)) {
		badPageQuery(w, "invalid node_id filter")
		return 0, "", pgstore.TelemetryFilter{}, "", false
	}
	instanceID, ok := one("instance_id")
	if !ok || (instanceID != "" && !validID(instanceID)) {
		badPageQuery(w, "invalid instance_id filter")
		return 0, "", pgstore.TelemetryFilter{}, "", false
	}
	now := time.Now().UTC()
	from, to := now.Add(-24*time.Hour), now
	if fromText != "" || toText != "" {
		if fromText == "" || toText == "" {
			badPageQuery(w, "from and to must be supplied together")
			return 0, "", pgstore.TelemetryFilter{}, "", false
		}
		var err error
		from, err = time.Parse(time.RFC3339Nano, fromText)
		if err != nil {
			badPageQuery(w, "from and to must be RFC3339 timestamps")
			return 0, "", pgstore.TelemetryFilter{}, "", false
		}
		to, err = time.Parse(time.RFC3339Nano, toText)
		if err != nil {
			badPageQuery(w, "from and to must be RFC3339 timestamps")
			return 0, "", pgstore.TelemetryFilter{}, "", false
		}
		from, to = from.UTC(), to.UTC()
	}
	if !from.Before(to) || to.Sub(from) > maxTelemetryQueryRange || from.Before(now.Add(-maxTelemetryQueryRange)) || to.After(now.Add(time.Minute)) {
		badPageQuery(w, "telemetry time range must be within the last 30 days and no more than one minute in the future")
		return 0, "", pgstore.TelemetryFilter{}, "", false
	}
	filter := pgstore.TelemetryFilter{From: from, To: to, NodeID: nodeID, InstanceID: instanceID}
	scope := telemetryCursorScope(filter)
	return pageSize, cursorText, filter, scope, true
}

func telemetryCursorScope(filter pgstore.TelemetryFilter) string {
	return strings.Join([]string{
		filter.From.UTC().Format(time.RFC3339Nano),
		filter.To.UTC().Format(time.RFC3339Nano),
		url.QueryEscape(filter.NodeID),
		url.QueryEscape(filter.InstanceID),
	}, "|")
}
