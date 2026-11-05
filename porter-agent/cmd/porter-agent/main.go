//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"porter/agent/internal/client"
	fcruntime "porter/agent/internal/runtime"
	"porter/agent/internal/store"
	"porter/protocol"
)

var Version = "v1.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: porter-agent join|run|version")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "join":
		join(os.Args[2:])
	case "run":
		if err := run(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Println("unknown command")
		os.Exit(2)
	}
}

func arg(args []string, name, def string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return def
}

func agentTLSOptions(args []string) client.TLSOptions {
	return client.TLSOptions{
		ServerCAFile:   arg(args, "--server-ca", os.Getenv("PORTER_AGENT_SERVER_CA")),
		ClientCertFile: arg(args, "--client-cert", os.Getenv("PORTER_AGENT_CLIENT_CERT")),
		ClientKeyFile:  arg(args, "--client-key", os.Getenv("PORTER_AGENT_CLIENT_KEY")),
	}
}

func join(args []string) {
	server := arg(args, "--server", "http://127.0.0.1:8080")
	token := arg(args, "--token", "")
	name := arg(args, "--name", hostname())
	dataDir := arg(args, "--data-dir", "/var/lib/porter-agent")
	kernel := arg(args, "--kernel", "/var/lib/porter-agent/vmlinux")
	rootfs := arg(args, "--rootfs", "/var/lib/porter-agent/rootfs.ext4")
	if token == "" {
		log.Fatal("--token is required")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	cpus := runtime.NumCPU()
	mem := memTotalMB()
	disk := diskFreeMB(dataDir)
	req := protocol.EnrollRequest{Token: token, Hostname: name, OS: "linux", Arch: runtime.GOARCH, CPUs: cpus, MemoryMB: mem, DiskMB: disk}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := client.JoinWithTLS(ctx, server, req, agentTLSOptions(args))
	if err != nil {
		log.Fatalf("join failed: %v", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "porter-agent.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveConfig(store.Config{Server: server, NodeID: resp.NodeID, NodeToken: resp.NodeToken, DataDir: dataDir, Kernel: kernel, Rootfs: rootfs}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("joined node %s\n", resp.NodeID)
	fmt.Println("run: sudo porter-agent run --data-dir", dataDir)
}

func run(args []string) error {
	dataDir := arg(args, "--data-dir", "/var/lib/porter-agent")
	st, err := store.Open(filepath.Join(dataDir, "porter-agent.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	cfg, err := st.LoadConfig()
	if err != nil || cfg.NodeID == "" {
		log.Fatal("agent not joined; run porter-agent join first")
	}
	cl, err := client.NewWithTLS(cfg.Server, cfg.NodeID, cfg.NodeToken, agentTLSOptions(args))
	if err != nil {
		return fmt.Errorf("configure control-plane TLS: %w", err)
	}
	netCfg := fcruntime.NetworkConfig{}
	if arg(args, "--network", "on") != "off" {
		netCfg = fcruntime.NetworkConfig{Bridge: "porter0", CIDR: "10.42.0.0/16", GatewayIP: "10.42.0.1", OutIface: "eth0", EnableNAT: true}
	}
	eng := fcruntime.New(filepath.Join(dataDir, "vms"), cfg.Kernel, cfg.Rootfs, netCfg)
	eng.InitPath = arg(args, "--init-path", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		cancel()
	}()
	log.Printf("porter-agent %s node=%s server=%s", Version, cfg.NodeID, cfg.Server)
	hb := time.NewTicker(2 * time.Second)
	defer hb.Stop()
	cmdTick := time.NewTicker(2 * time.Second)
	defer cmdTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hb.C:
			_ = cl.Heartbeat(ctx, protocol.Heartbeat{NodeID: cfg.NodeID, CPUsFree: runtime.NumCPU(), MemoryFreeMB: memAvailableMB(), DiskFreeMB: diskFreeMB(dataDir), VMCount: st.CountVMs(), At: time.Now().UTC()})
			for _, vm := range st.ListVMs() {
				if lines := eng.TailLog(vm, 30); len(lines) > 0 {
					_ = cl.PostLogs(ctx, vm.ID, lines)
				}
				if vm.State != "running" {
					continue
				}
				if !eng.Healthy(vm) {
					vm.State = "failed"
					vm.Health = "unhealthy"
					vm.Error = "firecracker process exited"
					_ = st.UpsertVM(vm)
					_ = cl.ReportVMState(ctx, vm.ID, "failed", "unhealthy", vm.Error)
					continue
				}
				if vm.GuestIP != "" && vm.Port > 0 {
					health := "unhealthy"
					if probe("http://" + vm.GuestIP + ":" + strconv.Itoa(vm.Port) + "/") {
						health = "healthy"
					}
					if health != vm.Health {
						vm.Health = health
						_ = st.UpsertVM(vm)
						_ = cl.ReportVMState(ctx, vm.ID, "running", health, "")
					}
				}
			}
		case <-cmdTick.C:
			cmds, err := pollCommands(ctx, cl, st)
			if err != nil {
				if isPermanentCommandResultError(err) {
					return fmt.Errorf("agent stopped; pending command result was rejected and retained locally, operator action required: %w", err)
				}
				log.Printf("commands: %v", err)
				continue
			}
			for _, c := range cmds {
				if err := handle(ctx, cl, eng, st, c); err != nil {
					return fmt.Errorf("agent stopped before polling more commands; result for command %s attempt %d requires operator action: %w", c.ID, c.Attempt, err)
				}
			}
		}
	}
}

type commandResultPoster interface {
	Result(context.Context, protocol.CommandResult) error
}

type commandControlPlane interface {
	commandResultPoster
	Commands(context.Context) ([]protocol.Command, error)
}

type commandHandlerClient interface {
	commandResultPoster
	RecordSnapshot(context.Context, map[string]interface{}) error
}

type httpStatusCoder interface {
	HTTPStatusCode() int
}

type permanentCommandResultError struct{ err error }

func (e *permanentCommandResultError) Error() string {
	return fmt.Sprintf("permanent command-result rejection; result retained locally: %v", e.err)
}

func (e *permanentCommandResultError) Unwrap() error { return e.err }

func isPermanentCommandResultError(err error) bool {
	var permanent *permanentCommandResultError
	return errors.As(err, &permanent)
}

func isPermanentHTTPStatusError(err error) bool {
	var statusErr httpStatusCoder
	if !errors.As(err, &statusErr) {
		return false
	}
	status := statusErr.HTTPStatusCode()
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	default:
		return true
	}
}

func pollCommands(ctx context.Context, cl commandControlPlane, st *store.Store) ([]protocol.Command, error) {
	if err := flushPendingCommandResults(ctx, cl, st); err != nil {
		return nil, err
	}
	return cl.Commands(ctx)
}

func flushPendingCommandResults(ctx context.Context, cl commandResultPoster, st *store.Store) error {
	pending, err := st.PendingCommandResults()
	if err != nil {
		return fmt.Errorf("read command result outbox: %w", err)
	}
	for _, result := range pending {
		if err := cl.Result(ctx, result); err != nil {
			postErr := fmt.Errorf("post result for command %s attempt %d: %w", result.CommandID, result.Attempt, err)
			if isPermanentHTTPStatusError(err) {
				return &permanentCommandResultError{err: postErr}
			}
			return postErr
		}
		if err := st.AckCommandResult(result.CommandID, result.Attempt); err != nil {
			return fmt.Errorf("ack result for command %s attempt %d: %w", result.CommandID, result.Attempt, err)
		}
	}
	return nil
}

func handle(ctx context.Context, cl commandHandlerClient, eng *fcruntime.Engine, st *store.Store, c protocol.Command) error {
	res := protocol.CommandResult{CommandID: c.ID, OperationID: c.OperationID, NodeID: c.NodeID, Attempt: c.Attempt, Success: true}
	raw, _ := json.Marshal(c.Payload)
	switch c.Kind {
	case "vm.apply":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		vm, err := applyVMIdempotent(ctx, st, spec, eng.Apply, eng.Healthy, eng.ProcessRunning)
		if err != nil {
			res.Success = false
			res.Error = err.Error()
		}
		res.State = vm.State
	case "vm.delete":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		if err := transitionVM(ctx, st, spec.VMID, "deleted", func(ctx context.Context, vm store.VM) error {
			return eng.Delete(ctx, vm)
		}); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		res.State = "deleted"
	case "vm.stop":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		if err := transitionVM(ctx, st, spec.VMID, "stopped", func(_ context.Context, vm store.VM) error {
			return eng.Stop(vm)
		}); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		res.State = "stopped"
	case "vm.start", "vm.restart":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		vm, err := runVMLifecycleCommand(ctx, st, c.Kind, spec, eng.Start, eng.Restart, eng.Healthy, eng.ProcessRunning)
		if err != nil {
			res.Success = false
			res.Error = err.Error()
		}
		res.State = vm.State
	case "volume.resize":
		var in protocol.VolumeResizeSpec
		if err := json.Unmarshal(raw, &in); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		if err := eng.ResizeVolume(ctx, in.VolumeID, in.SizeMB); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		res.State = "resized"
	case "snapshot.restore":
		var in struct {
			VMID        string `json:"vm_id"`
			StatePath   string `json:"state_path"`
			MemPath     string `json:"mem_path"`
			StateSHA256 string `json:"state_sha256"`
			MemSHA256   string `json:"mem_sha256"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		vm, ok := st.GetVM(in.VMID)
		if !ok {
			res.Success = false
			res.Error = "vm not found"
			break
		}
		nvm, err := eng.RestoreSnapshot(ctx, vm, in.StatePath, in.MemPath, in.StateSHA256, in.MemSHA256)
		if err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		_ = st.UpsertVM(nvm)
		res.State = "running"
	case "snapshot.create":
		var snap protocol.SnapshotSpec
		if err := json.Unmarshal(raw, &snap); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		vm, ok := st.GetVM(snap.VMID)
		if !ok {
			res.Success = false
			res.Error = "vm not found"
			break
		}
		statePath, memPath, size, err := eng.Snapshot(ctx, vm, snap.SnapshotID)
		if err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		stateSHA256, err := fileSHA256(statePath)
		if err != nil {
			res.Success = false
			res.Error = fmt.Sprintf("hash snapshot state: %v", err)
			break
		}
		memSHA256, err := fileSHA256(memPath)
		if err != nil {
			res.Success = false
			res.Error = fmt.Sprintf("hash snapshot memory: %v", err)
			break
		}
		if err := cl.RecordSnapshot(ctx, map[string]interface{}{"id": snap.SnapshotID, "vm_id": snap.VMID, "state_path": statePath, "mem_path": memPath, "state_sha256": stateSHA256, "mem_sha256": memSHA256, "size_bytes": size}); err != nil {
			res.Success = false
			res.Error = fmt.Sprintf("record snapshot: %v", err)
			break
		}
		res.State = "snapshot_ready"
	default:
		res.Success = false
		res.Error = "unknown command kind: " + c.Kind
	}
	if err := st.QueueCommandResult(res); err != nil {
		return fmt.Errorf("persist result for command %s attempt %d: %w", c.ID, c.Attempt, err)
	}
	if err := flushPendingCommandResults(ctx, cl, st); err != nil {
		if isPermanentCommandResultError(err) {
			return err
		}
		log.Printf("command result queued for retry: %v", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func transitionVM(ctx context.Context, st *store.Store, vmID, state string, cleanup func(context.Context, store.VM) error) error {
	if vmID == "" {
		return fmt.Errorf("vm id required")
	}
	vm, ok := st.GetVM(vmID)
	if !ok {
		return nil
	}
	if cleanup != nil {
		if err := cleanup(ctx, vm); err != nil {
			return fmt.Errorf("clean up vm %q: %w", vmID, err)
		}
	}
	vm.State = state
	vm.Health = ""
	vm.Error = ""
	if err := st.UpsertVM(vm); err != nil {
		return fmt.Errorf("persist vm %q %s state: %w", vmID, state, err)
	}
	return nil
}

func hostname() string { h, _ := os.Hostname(); return h }

func memTotalMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 4096
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.Atoi(f[1])
				return kb / 1024
			}
		}
	}
	return 4096
}

func memAvailableMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memTotalMB()
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.Atoi(fields[1])
				if err == nil && kb > 0 {
					return kb / 1024
				}
			}
		}
	}
	return memTotalMB()
}

func diskFreeMB(path string) int {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int(st.Bavail * uint64(st.Bsize) / (1024 * 1024))
}

func probe(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}
