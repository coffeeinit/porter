package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
)

func TestTelemetryQueriesRequireAdminAndApplyFilters(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	repo := &stubRepository{
		telemetryLogs: []v1.TelemetryLog{
			{ID: "2", NodeID: "node-a", InstanceID: "ins-a", ReportedAt: now, Level: "info", Message: "runtime started"},
			{ID: "1", NodeID: "node-a", InstanceID: "ins-a", ReportedAt: now.Add(-time.Second), Level: "warn", Message: "runtime delayed"},
		},
		telemetryMetrics: []v1.TelemetryMetric{{ID: "3", NodeID: "node-a", InstanceID: "ins-a", ReportedAt: now, Name: "firecracker.memory.rss_pages", Value: 42, Unit: "pages"}},
	}
	server := mustServer(t, repo)
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	query := "from=" + from + "&to=" + to + "&node_id=node-a&instance_id=ins-a&page_size=1"

	unauthorized := serve(server, http.MethodGet, "/api/v1/logs?"+query, "", "", "")
	if unauthorized.Code != http.StatusUnauthorized || repo.logFilter.NodeID != "" {
		t.Fatalf("unauthorized logs query status=%d filter=%#v", unauthorized.Code, repo.logFilter)
	}

	logs := serve(server, http.MethodGet, "/api/v1/logs?"+query, "Bearer "+testAdminToken, "", "")
	if logs.Code != http.StatusOK {
		t.Fatalf("logs status=%d body=%s", logs.Code, logs.Body.String())
	}
	var logPage v1.Page[v1.TelemetryLog]
	if err := json.Unmarshal(logs.Body.Bytes(), &logPage); err != nil {
		t.Fatal(err)
	}
	if len(logPage.Items) != 1 || logPage.NextCursor == "" || repo.logLimit != 2 || repo.logFilter.NodeID != "node-a" || repo.logFilter.InstanceID != "ins-a" || !repo.logFilter.From.Equal(now.Add(-time.Hour)) || !repo.logFilter.To.Equal(now) {
		t.Fatalf("logs page=%#v filter=%#v limit=%d", logPage, repo.logFilter, repo.logLimit)
	}

	metrics := serve(server, http.MethodGet, "/api/v1/metrics?"+query, "Bearer "+testAdminToken, "", "")
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", metrics.Code, metrics.Body.String())
	}
	var metricPage v1.Page[v1.TelemetryMetric]
	if err := json.Unmarshal(metrics.Body.Bytes(), &metricPage); err != nil {
		t.Fatal(err)
	}
	if len(metricPage.Items) != 1 || metricPage.Items[0].Name != "firecracker.memory.rss_pages" || repo.metricFilter.NodeID != "node-a" || repo.metricLimit != 2 {
		t.Fatalf("metrics page=%#v filter=%#v limit=%d", metricPage, repo.metricFilter, repo.metricLimit)
	}

	wrongScopeQuery := strings.Replace(query, "node_id=node-a", "node_id=node-b", 1)
	wrongScope := serve(server, http.MethodGet, "/api/v1/logs?"+wrongScopeQuery+"&cursor="+logPage.NextCursor, "Bearer "+testAdminToken, "", "")
	if wrongScope.Code != http.StatusBadRequest {
		t.Fatalf("wrong-scope cursor status=%d body=%s", wrongScope.Code, wrongScope.Body.String())
	}
}

func TestTelemetryQueryRejectsUnboundedOrMalformedTimeAndPagination(t *testing.T) {
	server := mustServer(t, &stubRepository{})
	auth := "Bearer " + testAdminToken
	now := time.Now().UTC()
	cases := []string{
		"from=" + now.Add(-31*24*time.Hour).Format(time.RFC3339) + "&to=" + now.Format(time.RFC3339),
		"from=" + now.Format(time.RFC3339),
		"from=" + now.Format(time.RFC3339) + "&from=" + now.Format(time.RFC3339) + "&to=" + now.Add(time.Hour).Format(time.RFC3339),
		"from=" + now.Format(time.RFC3339) + "&to=" + now.Add(2*time.Hour).Format(time.RFC3339),
		"page_size=101",
		"node_id=../other",
		"trace_id=anything",
	}
	for _, query := range cases {
		response := serve(server, http.MethodGet, "/api/v1/metrics?"+query, auth, "", "")
		if response.Code != http.StatusBadRequest {
			t.Errorf("query %q status=%d body=%s, want 400", query, response.Code, response.Body.String())
		}
	}
}
