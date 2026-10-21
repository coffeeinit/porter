package dns

import (
	"net"
	"testing"

	"porter/internal/types"
)

// testStore implements the Store interface for testing.
type testStore struct {
	vms      []*types.VM
	domains  []*types.Domain
	projects []*types.Project
}

func (s *testStore) GetVM(id string) (*types.VM, bool) {
	for _, v := range s.vms {
		if v.ID == id {
			return v, true
		}
	}
	return nil, false
}

func (s *testStore) ListVMs() []*types.VM           { return s.vms }
func (s *testStore) ListProjects() []*types.Project { return s.projects }

func (s *testStore) ListDomains(projectID string) []*types.Domain {
	var out []*types.Domain
	for _, d := range s.domains {
		if d.ProjectID == projectID {
			out = append(out, d)
		}
	}
	return out
}

func TestNewServer(t *testing.T) {
	store := &testStore{
		vms: []*types.VM{
			{ID: "1", ServiceName: "web", ProjectID: "myapp", IPAddress: "10.0.0.5"},
		},
	}
	srv := NewServer(store, "porter.test", net.ParseIP("127.0.0.1"))
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	if srv.baseDomain != "porter.test" {
		t.Fatalf("expected baseDomain 'porter.test', got %q", srv.baseDomain)
	}
}

func TestProjectSlug(t *testing.T) {
	tests := []struct {
		name     string
		project  *types.Project
		expected string
	}{
		{
			name:     "simple name",
			project:  &types.Project{Name: "My App"},
			expected: "my-app",
		},
		{
			name:     "special characters",
			project:  &types.Project{Name: "My_App!@#"},
			expected: "my-app",
		},
		{
			name:     "empty name",
			project:  &types.Project{Name: ""},
			expected: "app",
		},
		{
			name:     "multiple spaces",
			project:  &types.Project{Name: "My   App"},
			expected: "my-app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectSlug(tt.project)
			if got != tt.expected {
				t.Errorf("projectSlug() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestDomainManager(t *testing.T) {
	store := &testStore{
		projects: []*types.Project{
			{ID: "proj1", Name: "My App"},
		},
	}
	dm := NewDomainManager(nil, "porter.test", "1.2.3.4")
	if dm == nil {
		t.Fatal("NewDomainManager returned nil")
	}
	if dm.baseDomain != "porter.test" {
		t.Fatalf("expected baseDomain 'porter.test', got %q", dm.baseDomain)
	}

	// Test domain generation
	preview := dm.GetPreviewDomain(store.projects[0])
	if preview != "my-app.preview.porter.test" {
		t.Fatalf("expected preview domain 'my-app.preview.porter.test', got %q", preview)
	}

	prod := dm.GetProductionDomain(store.projects[0])
	if prod != "my-app.porter.test" {
		t.Fatalf("expected production domain 'my-app.porter.test', got %q", prod)
	}
}

func TestPtrToIP(t *testing.T) {
	tests := []struct {
		name     string
		ptr      string
		expected string
	}{
		{
			name:     "valid PTR",
			ptr:      "5.0.0.10.in-addr.arpa.",
			expected: "10.0.0.5",
		},
		{
			name:     "invalid PTR",
			ptr:      "invalid",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := ptrToIP(tt.ptr)
			if tt.expected == "" {
				if ip != nil {
					t.Errorf("expected nil, got %v", ip)
				}
			} else {
				if ip == nil {
					t.Errorf("expected %s, got nil", tt.expected)
				} else if ip.String() != tt.expected {
					t.Errorf("expected %s, got %s", tt.expected, ip.String())
				}
			}
		})
	}
}
