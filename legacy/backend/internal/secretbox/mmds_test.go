package secretbox

import "testing"

func TestMMDSPayloadShape(t *testing.T) {
	p := MMDSPayload("proj-1", map[string]string{"API_KEY": "v"})
	porter, ok := p["porter"].(map[string]any)
	if !ok {
		t.Fatal("missing porter envelope")
	}
	if porter["project_id"] != "proj-1" {
		t.Fatalf("project_id not carried: %v", porter)
	}
	secrets, ok := porter["secrets"].(map[string]any)
	if !ok || secrets["API_KEY"] != "v" {
		t.Fatalf("secrets not carried: %v", porter)
	}
}
