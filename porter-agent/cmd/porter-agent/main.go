//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
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
		run(os.Args[2:])
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
	resp, err := client.Join(ctx, server, req)
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

func run(args []string) {
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
	cl := client.New(cfg.Server, cfg.NodeID, cfg.NodeToken)
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
			return
		case <-hb.C:
			_ = cl.Heartbeat(ctx, protocol.Heartbeat{NodeID: cfg.NodeID, CPUsFree: runtime.NumCPU(), MemoryFreeMB: memTotalMB(), DiskFreeMB: diskFreeMB(dataDir), VMCount: st.CountVMs(), At: time.Now().UTC()})
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
			cmds, err := cl.Commands(ctx)
			if err != nil {
				log.Printf("commands: %v", err)
				continue
			}
			for _, c := range cmds {
				handle(ctx, cl, eng, st, c)
			}
		}
	}
}

func handle(ctx context.Context, cl *client.Client, eng *fcruntime.Engine, st *store.Store, c protocol.Command) {
	res := protocol.CommandResult{CommandID: c.ID, OperationID: c.OperationID, NodeID: c.NodeID, Success: true}
	raw, _ := json.Marshal(c.Payload)
	switch c.Kind {
	case "vm.apply":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		vm, err := eng.Apply(ctx, spec)
		if err != nil {
			res.Success = false
			res.Error = err.Error()
			vm = store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed", Error: err.Error(), GuestIP: spec.GuestIP, Port: spec.Port, Health: "unhealthy"}
		}
		if vm.GuestIP == "" {
			vm.GuestIP = spec.GuestIP
		}
		if vm.Port == 0 {
			vm.Port = spec.Port
		}
		if vm.Health == "" {
			vm.Health = "checking"
		}
		_ = st.UpsertVM(vm)
		res.State = vm.State
	case "vm.delete":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success = false
			res.Error = err.Error()
			break
		}
		if vm, ok := st.GetVM(spec.VMID); ok {
			_ = eng.Delete(ctx, vm)
			_ = st.UpsertVM(store.VM{ID: vm.ID, ReplicaID: vm.ReplicaID, State: "deleted", TapName: vm.TapName})
		}
	case "vm.stop":
		var spec protocol.VMApplySpec
		_ = json.Unmarshal(raw, &spec)
		if vm, ok := st.GetVM(spec.VMID); ok {
			_ = eng.Stop(vm)
			_ = st.UpsertVM(store.VM{ID: vm.ID, ReplicaID: vm.ReplicaID, State: "stopped", TapName: vm.TapName})
		}
		res.State = "stopped"
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
			VMID      string `json:"vm_id"`
			StatePath string `json:"state_path"`
			MemPath   string `json:"mem_path"`
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
		nvm, err := eng.RestoreSnapshot(ctx, vm, in.StatePath, in.MemPath)
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
		_ = cl.RecordSnapshot(ctx, map[string]interface{}{"id": snap.SnapshotID, "vm_id": snap.VMID, "state_path": statePath, "mem_path": memPath, "size_bytes": size})
		res.State = "snapshot_ready"
	default:
		res.Success = false
		res.Error = "unknown command kind: " + c.Kind
	}
	if err := cl.Result(ctx, res); err != nil {
		log.Printf("result: %v", err)
	}
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
