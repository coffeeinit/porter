//go:build linux

package main

import (
	"bufio"
	"context"
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
	"unsafe"

	"porter/agent/internal/bootstrap"
	"porter/agent/internal/client"
	"porter/agent/internal/localstate"
	fcruntime "porter/agent/internal/runtime"
	"porter/agent/internal/store"
	"porter/protocol"
)

var Version = "v1.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: porter-agent join --server https://<control-plane> [--hostname NAME] [--state-dir PATH] [--kernel PATH] [--rootfs PATH] | run [--state-dir PATH] [--kernel PATH] [--rootfs PATH] | version")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "join":
		if err := join(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
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

func parseJoinOptions(args []string) (bootstrap.Options, error) {
	var options bootstrap.Options
	options.StateDir = os.Getenv("PORTER_AGENT_STATE_DIR")
	options.Kernel = os.Getenv("PORTER_AGENT_KERNEL")
	options.Rootfs = os.Getenv("PORTER_AGENT_ROOTFS")
	options.TLS = client.TLSOptions{
		ServerCAFile:   os.Getenv("PORTER_AGENT_SERVER_CA"),
		ClientCertFile: os.Getenv("PORTER_AGENT_CLIENT_CERT"),
		ClientKeyFile:  os.Getenv("PORTER_AGENT_CLIENT_KEY"),
	}
	for i := 0; i < len(args); i++ {
		name := args[i]
		if name == "--token-stdin" {
			options.TokenStdin = true
			continue
		}
		if strings.HasPrefix(name, "--token") {
			return options, fmt.Errorf("token argument and token-file options are disabled; use hidden TTY input or --token-stdin")
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return options, fmt.Errorf("join option requires a value")
		}
		value := args[i+1]
		i++
		switch name {
		case "--server":
			options.Server = value
		case "--hostname":
			options.Hostname = value
		case "--state-dir":
			options.StateDir = value
		case "--kernel":
			options.Kernel = value
		case "--rootfs":
			options.Rootfs = value
		case "--server-ca":
			options.TLS.ServerCAFile = value
		case "--client-cert":
			options.TLS.ClientCertFile = value
		case "--client-key":
			options.TLS.ClientKeyFile = value
		default:
			return options, fmt.Errorf("unsupported join option")
		}
	}
	if options.Server == "" {
		return options, fmt.Errorf("--server with an HTTPS URL is required")
	}
	return options, nil
}

func join(args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("join must run as root; use sudo porter-agent join --server https://<control-plane>")
	}
	options, err := parseJoinOptions(args)
	if err != nil {
		return err
	}
	prepared, err := bootstrap.Prepare(options)
	if err != nil {
		return err
	}
	defer prepared.Close()
	token, err := readJoinToken(options.TokenStdin)
	if err != nil {
		_ = prepared.CancelBeforeSubmission()
		return err
	}
	if err := client.ValidateJoinToken(token); err != nil {
		_ = prepared.CancelBeforeSubmission()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := prepared.Enroll(ctx, token)
	if err != nil {
		return fmt.Errorf("join failed: %w", err)
	}
	fmt.Printf("joined node %s\n", resp.NodeID)
	fmt.Println("run: sudo porter-agent run --state-dir", prepared.StateDir())
	return nil
}

func readJoinToken(tokenStdin bool) (string, error) {
	if tokenStdin {
		if isTerminal(os.Stdin.Fd()) {
			return readHiddenTTYToken(os.Stdin)
		}
		return readProtectedStdinToken(os.Stdin)
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		defer tty.Close()
		return readHiddenTTYToken(tty)
	}
	return "", fmt.Errorf("could not open the controlling TTY; use --token-stdin with protected stdin for automation")
}

func isTerminal(fd uintptr) bool {
	var state syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&state)))
	return errno == 0
}

func readHiddenTTYToken(tty *os.File) (string, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(signals)
	var original syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&original)))
	if errno != 0 {
		return "", fmt.Errorf("could not secure hidden TTY input")
	}
	hidden := original
	hidden.Lflag &^= syscall.ECHO
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&hidden)))
	if errno != 0 {
		return "", fmt.Errorf("could not disable TTY echo for token input")
	}
	fmt.Fprint(tty, "Join token: ")
	type tokenRead struct {
		token string
		err   error
	}
	readDone := make(chan tokenRead, 1)
	go func() {
		token, err := readOneLine(tty)
		readDone <- tokenRead{token: token, err: err}
	}()
	restore := func() error {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&original)))
		if errno != 0 {
			return fmt.Errorf("could not restore TTY echo after token input")
		}
		return nil
	}
	select {
	case result := <-readDone:
		if err := restore(); err != nil {
			return "", err
		}
		fmt.Fprintln(tty)
		return result.token, result.err
	case caught := <-signals:
		if err := restore(); err != nil {
			return "", err
		}
		fmt.Fprintln(tty)
		return "", fmt.Errorf("join token input interrupted by %s", caught)
	}
}

func readProtectedStdinToken(input io.Reader) (string, error) {
	return readOneLine(input)
}

func readOneLine(input io.Reader) (string, error) {
	reader := bufio.NewReaderSize(input, 256)
	line, err := reader.ReadSlice('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("join token input is too long")
	}
	if len(line) > 128 {
		return "", fmt.Errorf("join token input is too long")
	}
	token := strings.TrimSuffix(string(line), "\n")
	token = strings.TrimSuffix(token, "\r")
	if token == "" {
		return "", fmt.Errorf("join token is required")
	}
	return token, nil
}

func run(args []string) error {
	if err := validateRunOptions(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("agent service must run as root")
	}
	stateDirDefault := os.Getenv("PORTER_AGENT_STATE_DIR")
	if stateDirDefault == "" {
		stateDirDefault = bootstrap.DefaultStateDir
	}
	stateDir, err := localstate.EnsurePrivateDir(arg(args, "--state-dir", stateDirDefault))
	if err != nil {
		return err
	}
	serviceConfig, err := localstate.ReadServiceConfig(filepath.Join(stateDir, "agent-service.json"))
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(stateDir, "porter-agent.db"))
	if err != nil {
		return fmt.Errorf("open agent state: %w", err)
	}
	defer st.Close()
	cfg, err := st.LoadConfig()
	if err != nil {
		return fmt.Errorf("read agent identity: %w", err)
	}
	if cfg.NodeID == "" || cfg.NodeID != serviceConfig.NodeID || cfg.Server != serviceConfig.Server || cfg.CredentialPath != serviceConfig.CredentialPath || cfg.CredentialPath != localstate.CredentialPath(stateDir) || filepath.Clean(serviceConfig.StateDir) != stateDir {
		return fmt.Errorf("agent identity metadata is incomplete or inconsistent; service will not start")
	}
	nodeToken, err := localstate.ReadCredential(cfg.CredentialPath)
	if err != nil {
		return err
	}
	tlsOptions := agentTLSOptions(args)
	if tlsOptions.ServerCAFile == "" {
		tlsOptions.ServerCAFile = serviceConfig.ServerCAFile
	}
	if tlsOptions.ClientCertFile == "" {
		tlsOptions.ClientCertFile = serviceConfig.ClientCertFile
	}
	if tlsOptions.ClientKeyFile == "" {
		tlsOptions.ClientKeyFile = serviceConfig.ClientKeyFile
	}
	cl, err := client.NewWithTLS(serviceConfig.Server, cfg.NodeID, nodeToken, tlsOptions)
	if err != nil {
		return fmt.Errorf("configure control-plane TLS: %w", err)
	}
	netCfg := fcruntime.NetworkConfig{}
	if arg(args, "--network", "on") != "off" {
		netCfg = fcruntime.NetworkConfig{Bridge: "porter0", CIDR: "10.42.0.0/16", GatewayIP: "10.42.0.1", OutIface: "eth0", EnableNAT: true}
	}
	runtimeKernel := serviceConfig.Kernel
	if fromEnv := os.Getenv("PORTER_AGENT_KERNEL"); fromEnv != "" {
		runtimeKernel = fromEnv
	}
	runtimeKernel = arg(args, "--kernel", runtimeKernel)
	runtimeRootfs := serviceConfig.Rootfs
	if fromEnv := os.Getenv("PORTER_AGENT_ROOTFS"); fromEnv != "" {
		runtimeRootfs = fromEnv
	}
	runtimeRootfs = arg(args, "--rootfs", runtimeRootfs)
	eng := fcruntime.New(filepath.Join(stateDir, "vms"), runtimeKernel, runtimeRootfs, netCfg)
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
			now := time.Now().UTC()
			_ = cl.Heartbeat(ctx, protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: now})
			metrics := []struct {
				name  string
				value float64
				unit  string
			}{
				{name: "porter.agent.cpu_count", value: float64(runtime.NumCPU()), unit: "count"},
				{name: "porter.agent.memory_available_mb", value: float64(memAvailableMB()), unit: "MiB"},
				{name: "porter.agent.disk_available_mb", value: float64(diskFreeMB(stateDir)), unit: "MiB"},
				{name: "porter.agent.vm_count", value: float64(st.CountVMs()), unit: "count"},
			}
			batch := protocol.TelemetryBatchV2{ProtocolVersion: protocol.VersionV2, NodeID: cfg.NodeID, Logs: []protocol.TelemetryLogV2{}, Metrics: []protocol.TelemetryMetricV2{}}
			for _, metric := range metrics {
				recordID, idErr := client.NewTelemetryRecordID()
				if idErr != nil {
					continue
				}
				batch.Metrics = append(batch.Metrics, protocol.TelemetryMetricV2{RecordID: recordID, ReportedAt: now, Name: metric.name, Value: metric.value, Unit: metric.unit})
			}
			_ = cl.Telemetry(ctx, batch)
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
					_ = postVMStateTelemetry(ctx, cl, cfg.NodeID, vm, now)
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
						_ = postVMStateTelemetry(ctx, cl, cfg.NodeID, vm, now)
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

func validateRunOptions(args []string) error {
	allowed := map[string]bool{
		"--state-dir": true, "--kernel": true, "--rootfs": true, "--server-ca": true,
		"--client-cert": true, "--client-key": true, "--network": true, "--init-path": true,
	}
	for i := 0; i < len(args); i++ {
		name := args[i]
		if !allowed[name] {
			return fmt.Errorf("unsupported run option")
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return fmt.Errorf("run option requires a value")
		}
		value := args[i+1]
		if name == "--network" && value != "on" && value != "off" {
			return fmt.Errorf("--network must be on or off")
		}
		i++
	}
	return nil
}

type commandResultPoster interface {
	Result(context.Context, protocol.CommandResultV2) error
}

type commandControlPlane interface {
	commandResultPoster
	Commands(context.Context) ([]protocol.CommandV2, error)
}

type commandHandlerClient interface {
	commandResultPoster
}

type commandRuntime interface {
	Apply(context.Context, protocol.VMApplySpec) (store.VM, error)
	Start(context.Context, protocol.VMApplySpec) (store.VM, error)
	Restart(context.Context, protocol.VMApplySpec) (store.VM, error)
	Stop(store.VM) error
	Delete(context.Context, store.VM) error
	Healthy(store.VM) bool
	ProcessRunning(store.VM) (bool, error)
	Snapshot(context.Context, store.VM, string) (protocol.SnapshotRecord, error)
	RestoreSnapshotV2(context.Context, store.VM, protocol.SnapshotRestoreCommand) (store.VM, protocol.SnapshotArtifactReceipt, error)
	ResizeVolume(context.Context, string, int) error
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

func pollCommands(ctx context.Context, cl commandControlPlane, st *store.Store) ([]protocol.CommandV2, error) {
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

func handle(ctx context.Context, cl commandHandlerClient, eng commandRuntime, st *store.Store, c protocol.CommandV2) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("reject invalid leased command: %w", err)
	}
	res := protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2,
		CommandID:       c.ID,
		OperationID:     c.OperationID,
		NodeID:          c.NodeID,
		Attempt:         c.Attempt,
		Kind:            c.Kind,
		LeaseToken:      c.LeaseToken,
		Success:         true,
	}
	raw := []byte(c.Payload)
	switch c.Kind {
	case "vm.apply":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		vm, err := applyVMIdempotent(ctx, st, spec, eng.Apply, eng.Healthy, eng.ProcessRunning)
		if err != nil {
			res.Success, res.Error = false, err.Error()
		}
		res.State = vm.State
	case "vm.delete":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		if err := transitionVM(ctx, st, spec.VMID, "deleted", func(ctx context.Context, vm store.VM) error {
			return eng.Delete(ctx, vm)
		}); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		res.State = "deleted"
	case "vm.stop":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		if err := transitionVM(ctx, st, spec.VMID, "stopped", func(_ context.Context, vm store.VM) error {
			return eng.Stop(vm)
		}); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		res.State = "stopped"
	case "vm.start", "vm.restart":
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		var vm store.VM
		var err error
		if c.Kind == "vm.start" {
			vm, err = runVMLifecycleCommand(ctx, st, c.Kind, spec, eng.Start, eng.Restart, eng.Healthy, eng.ProcessRunning)
		} else {
			vm, err = runVMLifecycleCommand(ctx, st, c.Kind, spec, eng.Start, eng.Restart, eng.Healthy, eng.ProcessRunning)
		}
		if err != nil {
			res.Success, res.Error = false, err.Error()
		}
		res.State = vm.State
	case "volume.resize":
		var spec protocol.VolumeResizeSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		if err := eng.ResizeVolume(ctx, spec.VolumeID, spec.SizeMB); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		res.State = "resized"
	case "snapshot.create":
		var spec protocol.SnapshotSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		vm, ok := st.GetVM(spec.VMID)
		if !ok {
			res.Success, res.Error = false, "vm not found"
			break
		}
		record, err := eng.Snapshot(ctx, vm, spec.SnapshotID)
		if err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		receipt, err := protocol.SnapshotArtifactReceiptFor(record)
		if err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		res.State = "snapshot_ready"
		res.SnapshotArtifact = &receipt
	case "snapshot.restore":
		var spec protocol.SnapshotRestoreCommand
		if err := json.Unmarshal(raw, &spec); err != nil {
			res.Success, res.Error = false, err.Error()
			break
		}
		vm, ok := st.GetVM(spec.VMID)
		if !ok {
			res.Success, res.Error = false, "vm not found"
			break
		}
		restored, receipt, err := eng.RestoreSnapshotV2(ctx, vm, spec)
		if err != nil {
			if persistErr := st.UpsertVM(restored); persistErr != nil {
				err = fmt.Errorf("%v; persist VM state after restore failure: %w", err, persistErr)
			}
			res.Success, res.Error = false, err.Error()
			break
		}
		if err := st.UpsertVM(restored); err != nil {
			res.Success = false
			res.Error = fmt.Sprintf("restore completed but persist VM state failed; runtime state may be running: %v", err)
			break
		}
		res.State = "restored"
		res.SnapshotArtifact = &receipt
	default:
		res.Success, res.Error = false, "unsupported command kind"
	}
	if !res.Success {
		res.State = "failed"
		if strings.TrimSpace(res.Error) == "" {
			res.Error = "agent command failed"
		}
		res.SnapshotArtifact = nil
	}
	if err := res.Validate(); err != nil {
		return fmt.Errorf("refuse invalid v2 command result: %w", err)
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

func postVMStateTelemetry(ctx context.Context, cl *client.Client, nodeID string, vm store.VM, at time.Time) error {
	recordID, err := client.NewTelemetryRecordID()
	if err != nil {
		return err
	}
	level := "info"
	if vm.State == "failed" || vm.Health == "unhealthy" {
		level = "error"
	}
	message := fmt.Sprintf("VM state update: state=%s health=%s", vm.State, vm.Health)
	if vm.Error != "" {
		message += " error=" + vm.Error
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	return cl.Telemetry(ctx, protocol.TelemetryBatchV2{
		ProtocolVersion: protocol.VersionV2,
		NodeID:          nodeID,
		Logs:            []protocol.TelemetryLogV2{{RecordID: recordID, InstanceID: vm.ID, ReportedAt: at, Level: level, Message: message}},
		Metrics:         []protocol.TelemetryMetricV2{},
	})
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
