package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"porter/internal/resource"
	"porter/internal/types"
)

// TestVPSWorkloadTypePG covers the persistent VPS flip (WP1): column +
// projects.data JSON sync in SetProjectWorkloadType, the microvm default for
// legacy rows, the org-wide listing with replica counts, and the validation
// / not-found error paths. Requires Postgres (skips without
// PORTER_TEST_DATABASE_URL, same convention as the billing tests).
func TestVPSWorkloadTypePG(t *testing.T) {
	s := pgStore(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	orgID := uuid.NewString()
	if err := s.PutOrg(&types.Org{ID: orgID, Name: "vps-org-" + suffix}); err != nil {
		t.Fatalf("put org: %v", err)
	}
	otherOrgID := uuid.NewString()
	if err := s.PutOrg(&types.Org{ID: otherOrgID, Name: "vps-org-other-" + suffix}); err != nil {
		t.Fatalf("put other org: %v", err)
	}
	projID := uuid.NewString()
	s.PutProject(&types.Project{ID: projID, Name: "vps-proj-" + suffix, OrgID: orgID})
	otherProjID := uuid.NewString()
	s.PutProject(&types.Project{ID: otherProjID, Name: "vps-proj-other-" + suffix, OrgID: otherOrgID})
	// porter_test persists across runs (pg-go.sh never drops it) and other
	// tests assert global row counts, so remove every fixture row. Direct
	// deletes, not DeleteOrgCascade — see the note in TestVMStaticIPPinPG.
	t.Cleanup(func() {
		s.DeleteReplicasByProject(projID)
		s.DeleteProject(projID)
		s.DeleteProject(otherProjID)
		_, _ = s.pool.Exec(context.Background(),
			`DELETE FROM orgs WHERE id = $1 OR id = $2`, orgID, otherOrgID)
	})

	// Rows never flipped read back as microvm/non-persistent via the 0036
	// column defaults.
	wt, persistent, found := s.GetProjectWorkloadType(projID)
	if !found || wt != types.WorkloadTypeMicroVM || persistent {
		t.Fatalf("default workload = (%q,%v,%v), want (microvm,false,true)", wt, persistent, found)
	}
	if _, _, found := s.GetProjectWorkloadType(uuid.NewString()); found {
		t.Fatal("unknown project must not be found")
	}

	// One replica so the org listing reports a real VM count.
	vmID := uuid.NewString()
	s.PutVM(&types.VM{ID: vmID, ProjectID: projID, Name: "vps-vm-" + suffix,
		State: types.StateRunning, HealthStatus: resource.HealthChecking})

	if err := s.SetProjectWorkloadType(projID, types.WorkloadTypeVPS, true); err != nil {
		t.Fatalf("set workload type: %v", err)
	}
	// Idempotent: an identical repeat flip must succeed.
	if err := s.SetProjectWorkloadType(projID, types.WorkloadTypeVPS, true); err != nil {
		t.Fatalf("repeat flip: %v", err)
	}
	wt, persistent, found = s.GetProjectWorkloadType(projID)
	if !found || wt != types.WorkloadTypeVPS || !persistent {
		t.Fatalf("after flip workload = (%q,%v,%v), want (vps,true,true)", wt, persistent, found)
	}

	// The projects.data JSON blob (GetProject's source of truth) must agree
	// with the columns — one write moves both.
	proj, ok := s.GetProject(projID)
	if !ok {
		t.Fatal("project missing after flip")
	}
	if proj.WorkloadType != types.WorkloadTypeVPS || !proj.Persistent {
		t.Fatalf("JSON blob out of sync: workload_type=%q persistent=%v", proj.WorkloadType, proj.Persistent)
	}
	if floor := proj.ReplicaFloor(); floor != 1 {
		t.Fatalf("persistent project ReplicaFloor = %d, want 1", floor)
	}

	got := s.ListVPSProjects(orgID)
	if len(got) != 1 || got[0].ID != projID || got[0].Name != "vps-proj-"+suffix || got[0].VMCount != 1 {
		t.Fatalf("ListVPSProjects = %+v, want [%s] with 1 vm", got, projID)
	}
	if list := s.ListVPSProjects(otherOrgID); len(list) != 0 {
		t.Fatalf("other org must not see the vps project: %+v", list)
	}

	// Validation and not-found paths.
	if err := s.SetProjectWorkloadType(projID, "bogus", true); err == nil {
		t.Fatal("expected error for invalid workload type")
	}
	if err := s.SetProjectWorkloadType(uuid.NewString(), types.WorkloadTypeVPS, true); err == nil {
		t.Fatal("expected not-found error for unknown project")
	}

	// Converting back clears persistence everywhere.
	if err := s.SetProjectWorkloadType(projID, types.WorkloadTypeMicroVM, false); err != nil {
		t.Fatalf("convert back: %v", err)
	}
	if wt, persistent, _ := s.GetProjectWorkloadType(projID); wt != types.WorkloadTypeMicroVM || persistent {
		t.Fatalf("after convert back = (%q,%v), want (microvm,false)", wt, persistent)
	}
	if proj, _ := s.GetProject(projID); proj.WorkloadType != types.WorkloadTypeMicroVM || proj.Persistent {
		t.Fatalf("JSON blob out of sync after convert back: workload_type=%q persistent=%v",
			proj.WorkloadType, proj.Persistent)
	}
	if list := s.ListVPSProjects(orgID); len(list) != 0 {
		t.Fatalf("converted-back project still listed as vps: %+v", list)
	}
}

// TestVMStaticIPPinPG covers the ip_allocations.pinned flag from migration
// 0036: pinning a VM's current allocation, per-IP pinning, the explicit
// error when there is nothing to pin, unpinning, and the per-project pinned
// listing. Requires Postgres.
func TestVMStaticIPPinPG(t *testing.T) {
	s := pgStore(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	orgID := uuid.NewString()
	if err := s.PutOrg(&types.Org{ID: orgID, Name: "pin-org-" + suffix}); err != nil {
		t.Fatalf("put org: %v", err)
	}
	projID := uuid.NewString()
	s.PutProject(&types.Project{ID: projID, Name: "pin-proj-" + suffix, OrgID: orgID})

	vmID := uuid.NewString()
	const ip1 = "10.42.7.5"
	s.PutVM(&types.VM{ID: vmID, ProjectID: projID, Name: "pin-vm-" + suffix,
		State: types.StateRunning, HealthStatus: resource.HealthChecking, IPAddress: ip1})
	if _, err := s.AllocateIP("net-"+suffix, ip1, vmID); err != nil {
		t.Fatalf("allocate ip: %v", err)
	}
	// Same fixture hygiene as TestVPSWorkloadTypePG; ReleaseIP also clears the
	// ip_allocations rows (TEXT-keyed, no FK into projects/replicas). Direct
	// deletes instead of DeleteOrgCascade: its replicas/domains statements
	// carry an unbalanced parenthesis, so the whole cascade errors and rolls
	// back without deleting anything.
	t.Cleanup(func() {
		_ = s.ReleaseIP(vmID)
		s.DeleteVM(vmID)
		s.DeleteProject(projID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM orgs WHERE id = $1`, orgID)
	})

	if err := s.PinVMStaticIP(vmID, ""); err != nil {
		t.Fatalf("pin current allocation: %v", err)
	}
	pinned := s.ListPinnedVMIPs(projID)
	if len(pinned) != 1 || pinned[0].VMID != vmID || pinned[0].IP != ip1 {
		t.Fatalf("pinned = %+v, want [{%s %s}]", pinned, vmID, ip1)
	}

	// Per-IP pin on a second allocation of the same VM.
	const ip2 = "10.42.7.6"
	if _, err := s.AllocateIP("net2-"+suffix, ip2, vmID); err != nil {
		t.Fatalf("allocate second ip: %v", err)
	}
	if err := s.PinVMStaticIP(vmID, ip2); err != nil {
		t.Fatalf("pin by ip: %v", err)
	}
	if got := s.ListPinnedVMIPs(projID); len(got) != 2 {
		t.Fatalf("expected both allocations pinned, got %+v", got)
	}

	// A VM without any allocation (or an unallocated ip) pins nothing and
	// says so — never fake success (SRS §66).
	if err := s.PinVMStaticIP(uuid.NewString(), ""); err == nil {
		t.Fatal("expected error pinning a VM without allocations")
	}
	if err := s.PinVMStaticIP(vmID, "203.0.113.1"); err == nil {
		t.Fatal("expected error pinning an unallocated ip")
	}

	if err := s.UnpinVMStaticIP(vmID); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if got := s.ListPinnedVMIPs(projID); len(got) != 0 {
		t.Fatalf("pinned after unpin = %+v, want empty", got)
	}
}
