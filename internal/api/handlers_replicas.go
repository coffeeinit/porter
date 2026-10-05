// Replicas/VMs, console, SSH, exec, snapshots/restore, aux VMs.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"porter/internal/agent"
	"porter/internal/auxvm"
	"porter/internal/cmdgate"
	"porter/internal/judge"
	"porter/internal/ptyd"
	"porter/internal/scheduler"
	"porter/internal/store"
	"porter/internal/types"
)

func replicaIndex(r *http.Request) int {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		return -1
	}
	return n
}

func (a *API) mutateReplicas(projID string, idxFilter func(int) bool, state string) (n int, failures []string) {
	proj, ok := a.store.GetProject(projID)
	if !ok {
		return 0, []string{"project not found"}
	}
	for i, vmID := range proj.VMIDs {
		if idxFilter != nil && !idxFilter(i) {
			continue
		}
		vm, ok := a.store.GetVM(vmID)
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: replica not found", vmID))
			continue
		}
		var err error
		switch state {
		case "stop":
			err = a.vmm.Stop(context.Background(), vm)
		case "start":
			err = a.vmm.Boot(context.Background(), vm)
		case "restart":
			err = a.vmm.Restart(context.Background(), vm)
		case "pause":
			err = a.vmm.Pause(context.Background(), vm)
		case "resume":
			err = a.vmm.Resume(context.Background(), vm)
		case "reboot":
			err = a.vmm.Reboot(context.Background(), vm)
		default:
			err = fmt.Errorf("unsupported replica action %q", state)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", vmID, err))
			continue
		}
		n++
	}
	return n, failures
}

// ============================================================================
// Replicas (project + global + VM compat)
// ============================================================================

func (a *API) handleListReplicas(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	vms := make([]*types.VM, 0, len(proj.VMIDs))
	for _, id := range proj.VMIDs {
		if vm, ok := a.store.GetVM(id); ok {
			vms = append(vms, vm)
		}
	}
	writeJSON(w, http.StatusOK, paginate(w, r, vms))
}

func (a *API) handleGetReplica(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	idx := replicaIndex(r)
	if idx < 0 || idx >= len(proj.VMIDs) {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	vm, ok := a.store.GetVM(proj.VMIDs[idx])
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	writeJSON(w, http.StatusOK, vm)
}

func (a *API) handleReplicaBatchStart(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), nil, "start")
	status := http.StatusAccepted
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"started": n, "failed": failures})
}

func (a *API) handleReplicaBatchStop(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), nil, "stop")
	status := http.StatusOK
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"stopped": n, "failed": failures})
}

func (a *API) handleReplicaStart(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), idxFilterAt(replicaIndex(r)), "start")
	status := http.StatusAccepted
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"started": n == 1, "failed": failures})
}

func (a *API) handleReplicaStop(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), idxFilterAt(replicaIndex(r)), "stop")
	status := http.StatusOK
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"stopped": n == 1, "failed": failures})
}

func (a *API) handleReplicaRestart(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), idxFilterAt(replicaIndex(r)), "restart")
	status := http.StatusOK
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"restarted": n == 1, "failed": failures})
}

func (a *API) handleReplicaPause(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), idxFilterAt(replicaIndex(r)), "pause")
	status := http.StatusOK
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"paused": n == 1, "failed": failures})
}

func (a *API) handleReplicaResume(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), idxFilterAt(replicaIndex(r)), "resume")
	status := http.StatusOK
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"resumed": n == 1, "failed": failures})
}

func (a *API) handleReplicaReboot(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), idxFilterAt(replicaIndex(r)), "reboot")
	status := http.StatusOK
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"rebooted": n == 1, "failed": failures})
}

func (a *API) handleListAllVMs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"vms": a.store.ListVMs()})
}

func (a *API) handleGetVMCompat(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	writeJSON(w, http.StatusOK, vm)
}

func (a *API) handleVMCompatDelete(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	if a.vmm != nil {
		_ = a.vmm.Delete(context.Background(), vm)
	}
	a.store.DeleteVM(vm.ID)
	if proj, pok := a.store.GetProject(vm.ProjectID); pok {
		kept := proj.VMIDs[:0]
		for _, id := range proj.VMIDs {
			if id != vm.ID {
				kept = append(kept, id)
			}
		}
		proj.VMIDs = kept
		a.store.PutProject(proj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleReplicaStartByID(w http.ResponseWriter, r *http.Request) {
	a.replicaActionByID(w, r, "start")
}

func (a *API) handleReplicaStopByID(w http.ResponseWriter, r *http.Request) {
	a.replicaActionByID(w, r, "stop")
}

func (a *API) handleReplicaRestartByID(w http.ResponseWriter, r *http.Request) {
	a.replicaActionByID(w, r, "restart")
}

func (a *API) handleReplicaPauseByID(w http.ResponseWriter, r *http.Request) {
	a.replicaActionByID(w, r, "pause")
}

func (a *API) handleReplicaResumeByID(w http.ResponseWriter, r *http.Request) {
	a.replicaActionByID(w, r, "resume")
}

func (a *API) handleReplicaRebootByID(w http.ResponseWriter, r *http.Request) {
	a.replicaActionByID(w, r, "reboot")
}

func (a *API) replicaActionByID(w http.ResponseWriter, r *http.Request, state string) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	n, failures := a.mutateReplicas(vm.ProjectID, idxFilterAt(vm.ReplicaIndex), state)
	status := http.StatusAccepted
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{state + "ed": n == 1, "failed": failures})
}

func (a *API) handleReplicaDelete(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	idx := replicaIndex(r)
	if idx < 0 || idx >= len(proj.VMIDs) {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	if vm, ok := a.store.GetVM(proj.VMIDs[idx]); ok {
		_ = a.vmm.Delete(context.Background(), vm)
	}
	proj.VMIDs = append(proj.VMIDs[:idx], proj.VMIDs[idx+1:]...)
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func idxFilterAt(idx int) func(int) bool {
	if idx < 0 {
		return nil
	}
	return func(i int) bool { return i == idx }
}

func (a *API) handleReplicaLogs(w http.ResponseWriter, r *http.Request) {
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	if r.URL.Query().Get("history") == "pg" {
		var before int64
		_, _ = fmt.Sscanf(r.URL.Query().Get("before"), "%d", &before)
		writeJSON(w, http.StatusOK, map[string]any{"logs": a.store.VMLogTail(vmID, tailN(r), before)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": a.store.TailLogs(vmID, tailN(r))})
}

func (a *API) handleReplicaLogStream(w http.ResponseWriter, r *http.Request) {
	vmID := r.PathValue("replicaId")
	if _, ok := a.store.GetVM(vmID); !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	serveLogStream(w, r, func() logStreamPayload {
		vm, found := a.store.GetVM(vmID)
		if !found {
			return logStreamPayload{Source: "replica", Status: "missing"}
		}
		return logStreamPayload{Source: "replica", Lines: a.store.TailLogs(vmID, 300), Status: string(vm.State)}
	}, nil)
}

func (a *API) handleReplicaMetrics(w http.ResponseWriter, r *http.Request) {
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	writeJSON(w, http.StatusOK, a.store.ListMetrics(vmID, 60))
}

func (a *API) handleReplicaTraffic(w http.ResponseWriter, r *http.Request) {
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	writeJSON(w, http.StatusOK, a.store.ListTraffic(vmID, 100))
}

func (a *API) handleReplicaHealth(w http.ResponseWriter, r *http.Request) {
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	writeJSON(w, http.StatusOK, map[string]any{"replica": vmID, "events": a.store.ListHealthEvents(a.projectID(r), 20)})
}

func (a *API) handleSSHInfo(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(a.vmAtReplica(a.projectID(r), replicaIndex(r)))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": vm.IPAddress, "port": 22, "user": "root"})
}

func (a *API) handleSSHCert(w http.ResponseWriter, r *http.Request) {
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	if execer, ok := a.vmm.(Execer); ok && vmID != "" {
		_ = execer
		writeJSON(w, http.StatusOK, map[string]any{"status": "ssh via guest agent", "replica": vmID, "host": vmInfoIP(a.store, vmID), "port": 22, "user": "root"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ssh unsupported until the guest-vsock agent is enabled", "replica": vmID})
}

func (a *API) handleReplicaExec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd []string `json:"cmd"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	if vmID == "" {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	if pattern, bad := cmdgate.DeniedInCode(req.Cmd); bad {
		a.store.AppendDaemonLog("exec deny vm=" + vmID + " pattern=" + pattern + " actor=" + currentUser(r))
		writeError(w, http.StatusForbidden, "command denied by safety gate: "+pattern)
		return
	}
	if key := os.Getenv("PORTER_TYPESAFE_KEY"); key != "" {
		rep, err := cmdgate.Check(r.Context(), &judge.Client{Key: key}, a.projectID(r), req.Cmd)
		if err != nil {
			writeError(w, http.StatusBadGateway, "safety judgment failed: "+err.Error())
			return
		}
		a.store.AppendDaemonLog("exec gate vm=" + vmID + " decision=" + string(rep.Decision) + " actor=" + currentUser(r))
		switch rep.Decision {
		case cmdgate.DecisionDeny, cmdgate.DecisionRequireApproval:
			writeError(w, http.StatusForbidden, "command blocked by safety gate: "+rep.Reason)
			return
		}
	}
	execer, ok := a.vmm.(Execer)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "exec unsupported", "replica": vmID, "cmd": req.Cmd})
		return
	}
	out := &bytes.Buffer{}
	err := execer.Exec(context.Background(), vmID, req.Cmd, strings.NewReader(""), out)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "exec error", "output": out.String(), "error": err.Error(), "replica": vmID})
		return
	}
	a.store.AppendLog(vmID, out.String())
	if r.URL.Query().Get("stream") == "sse" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		serveLogStream(w, r, func() logStreamPayload {
			return logStreamPayload{Source: "exec", Lines: strings.Split(strings.TrimRight(out.String(), "\n"), "\n"), Status: "done"}
		}, func(status string) bool { return status == "done" })
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "exec", "output": out.String(), "replica": vmID})
}

func (a *API) handleReplicaConsole(w http.ResponseWriter, r *http.Request) {
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	if vmID == "" {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	if r.URL.Query().Get("stream") == "sse" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		serveLogStream(w, r, func() logStreamPayload {
			return logStreamPayload{Source: "console", Lines: a.store.TailLogs(vmID, tailN(r)), Status: "streaming"}
		}, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "console via exec (non-interactive)", "replica": vmID, "stream": "GET ?stream=sse", "session": a.openConsoleSession(vmID, currentUser(r))})
}

func (a *API) openConsoleSession(vmID, principal string) map[string]any {
	if a.ptydReg == nil {
		a.ptydReg = ptyd.NewRegistry(4)
	}
	s := ptyd.Session{ID: store.NewID(), VMID: vmID, Principal: principal, OpenedAt: time.Now(), Cols: 80, Rows: 24}
	if err := a.ptydReg.Open(s); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"id": s.ID, "scope": ptyd.Scope, "port": ptyd.Port}
}

func vmInfoIP(st *store.Store, id string) string {
	if vm, ok := st.GetVM(id); ok {
		return vm.IPAddress
	}
	return ""
}

func (a *API) vmAtReplica(projID string, idx int) string {
	if proj, ok := a.store.GetProject(projID); ok && idx >= 0 && idx < len(proj.VMIDs) {
		return proj.VMIDs[idx]
	}
	return ""
}

func (a *API) handleGlobalReplicas(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListVMs())
}

func (a *API) handleGlobalReplica(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	writeJSON(w, http.StatusOK, vm)
}

func (a *API) handleReplicaLogsByID(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"logs": a.store.TailLogs(r.PathValue("replicaId"), tailN(r))})
}

func (a *API) handleReplicaMetricsByID(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListMetrics(r.PathValue("replicaId"), 60))
}

func (a *API) handleReplicaTrafficByID(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListTraffic(r.PathValue("replicaId"), 100))
}

func (a *API) handleReplicaHealthByID(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"replica": vm.ID, "running": vm.State == types.StateRunning,
		"state": vm.State, "health": vm.HealthStatus,
		"error": vm.Error,
	})
}

func (a *API) handleSSHInfoByID(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": vm.IPAddress, "port": 22, "user": "root"})
}

func (a *API) handleSSHCertByID(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	exe, ok := a.vmm.(Execer)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ssh unsupported until the guest-vsock agent is enabled", "replica": vm.ID, "host": vm.IPAddress, "port": 22})
		return
	}
	_ = exe
	writeJSON(w, http.StatusOK, map[string]any{"status": "ssh available via guest agent", "replica": vm.ID, "host": vm.IPAddress, "user": "root", "port": 22})
}

func (a *API) handleReplicaExecByID(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	var req struct {
		Cmd []string `json:"cmd"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if len(req.Cmd) == 0 {
		writeError(w, http.StatusBadRequest, "cmd is required")
		return
	}
	execer, ok := a.vmm.(Execer)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "exec unsupported for this replica", "replica": vm.ID, "cmd": req.Cmd})
		return
	}
	stdin := strings.NewReader("")
	stdout := &bytes.Buffer{}
	err := execer.Exec(context.Background(), vm.ID, req.Cmd, stdin, stdout)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "exec error", "output": stdout.String(), "error": err.Error(), "replica": vm.ID})
		return
	}
	a.store.AppendLog(vm.ID, strings.TrimSpace(stdout.String()))
	writeJSON(w, http.StatusOK, map[string]any{"status": "exec", "output": stdout.String(), "replica": vm.ID})
}

func (a *API) handleReplicaConsoleByID(w http.ResponseWriter, r *http.Request) {
	vmID := r.PathValue("replicaId")
	if _, ok := a.store.GetVM(vmID); !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	if r.URL.Query().Get("stream") == "sse" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		serveLogStream(w, r, func() logStreamPayload {
			return logStreamPayload{Source: "console", Lines: a.store.TailLogs(vmID, tailN(r)), Status: "streaming"}
		}, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "console via exec (non-interactive)", "replica": vmID, "stream": "GET ?stream=sse"})
}

func (a *API) handleSearchReplicaLogs(w http.ResponseWriter, r *http.Request) {
	vmID := a.vmAtReplica(a.projectID(r), replicaIndex(r))
	if vmID == "" {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": a.store.SearchVMLogs(vmID, q, tailN(r))})
}

func (a *API) handleMintConsoleToken(w http.ResponseWriter, r *http.Request) {
	var req consoleTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid console-token body")
		return
	}
	if req.Scope == "" {
		req.Scope = agent.ScopeTerminal
	}
	vmID := r.PathValue("replicaId")
	tok, err := agent.MintConsoleToken([]byte(a.secretKeyMaterial), vmID, req.Scope, 5*time.Minute)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": tok, "scope": req.Scope, "expires_in": 300,
	})
}

type consoleTokenReq struct {
	Scope string `json:"scope"`
}

func (a *API) handleGetPlacement(w http.ResponseWriter, r *http.Request) {
	p, ok := a.store.ActivePlacement(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "no active placement")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *API) handleMigrateVM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	vmID := r.PathValue("replicaId")
	source := ""
	if p, ok := a.store.ActivePlacement(vmID); ok {
		source = p.NodeID
	}
	if req.Target == "" {
		var nodes []scheduler.Node
		for _, s := range a.store.ListServers() {
			if s == nil {
				continue
			}
			st := strings.ToLower(s.Status)
			nodes = append(nodes, scheduler.Node{
				ID: s.ID, Ready: st == "ready" || st == "online" || st == "active",
				Arch: s.Arch, VCPUFree: s.VCPUs, MemFreeMiB: s.MemMiB,
			})
		}
		req.Target = scheduler.PickNode(nodes, scheduler.Request{})
		if req.Target == "" {
			writeError(w, http.StatusConflict, "no schedulable target node")
			return
		}
	} else {
		known := false
		for _, s := range a.store.ListServers() {
			if s != nil && (s.ID == req.Target || s.Name == req.Target) {
				known = true
				break
			}
		}
		if !known {
			writeError(w, http.StatusBadRequest, "unknown target node")
			return
		}
	}
	opID, err := a.store.CreateOperationWithPayload("migrate", "vm", vmID, "migrate|"+vmID+"|"+req.Target,
		map[string]string{"source": source, "target": req.Target})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "record migration")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED", "target": req.Target})
}

func (a *API) handleListAuxVMs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"aux": a.store.ListAuxVMs(r.PathValue("replicaId"))})
}

func (a *API) handleProvisionAuxVM(w http.ResponseWriter, r *http.Request) {
	workloadID := r.PathValue("replicaId")
	workload, ok := a.store.GetVM(workloadID)
	if !ok {
		writeError(w, http.StatusNotFound, "workload not found")
		return
	}
	var req struct {
		Kind  string `json:"kind"`
		Image string `json:"image"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Kind == auxvm.KindBrowser && req.Image == "" {
		writeError(w, http.StatusBadRequest, "browser auxVM requires image in request body")
		return
	}
	aux := auxvm.AuxVM{ID: "aux-" + store.NewID(), WorkloadID: workloadID, Kind: req.Kind}
	if err := aux.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.store.RecordAuxVM(aux.ID, aux.WorkloadID, aux.Kind, ""); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	boot := "deferred: runtime unavailable"
	if a.vmm != nil {
		guest := &types.VM{
			ID: aux.ID, Name: aux.ID,
			ProjectID:  workload.ProjectID,
			Image:      workload.Image,
			RootfsPath: workload.RootfsPath,
			VCPUs:      1,
			MemMiB:     512,
		}
		if req.Kind == auxvm.KindBrowser {
			guest.Image = req.Image
			guest.RootfsPath = ""
		}
		if err := a.vmm.Boot(r.Context(), guest); err != nil {
			boot = "failed: " + err.Error()
		} else {
			boot = "started"
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"aux": aux, "boot": boot})
}

func (a *API) handleDeleteAuxVM(w http.ResponseWriter, r *http.Request) {
	if !a.store.DeleteAuxVM(r.PathValue("auxId")) {
		writeError(w, http.StatusNotFound, "aux VM not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleAuxCDP(w http.ResponseWriter, r *http.Request) {
	rows := a.store.ListAuxVMs(r.PathValue("replicaId"))
	var found *store.AuxVMRow
	for i := range rows {
		if rows[i].ID == r.PathValue("auxId") {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "aux VM not found")
		return
	}
	if found.Kind != auxvm.KindBrowser {
		writeError(w, http.StatusBadRequest, "CDP is a browser auxVM capability")
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	rule := auxvm.CDPRule{AuxVMID: found.ID, Paired: true, AllowedIP: []string{host}}
	if host != "127.0.0.1" && host != "::1" {
		rule.Paired = false
	}
	if !rule.Authorize(host) {
		writeError(w, http.StatusForbidden, "CDP not authorized for source")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "authorized", "aux": found.ID})
}

func vmLive(v *types.VM) bool {
	switch v.State {
	case "", types.StateStopped, types.StateFailed, types.StateDeleting, types.StateDeleted:
		return false
	}
	return true
}

// recovered from internal/api/handlers.go
func (a *API) handleReplicaRestore(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /projects/{projectId}/replicas/{n}/recover, POST /projects/{projectId}/replicas/{n}/restore")
}

// recovered from internal/api/handlers.go
func (a *API) handleReplicaRestoreByID(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /vms/{replicaId}/recover, POST /vms/{replicaId}/restore")
}
