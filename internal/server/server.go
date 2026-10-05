package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"porter/internal/api"
	"porter/internal/auth"
	"porter/internal/cache"
	"porter/internal/config"
	"porter/internal/controller"
	cronrunner "porter/internal/cron"
	"porter/internal/dns"
	"porter/internal/event"
	"porter/internal/gateway"
	"porter/internal/health"
	"porter/internal/imagecatalog"
	"porter/internal/kernel"
	"porter/internal/logging"
	"porter/internal/metrics"
	"porter/internal/netmgr"
	"porter/internal/notify"
	"porter/internal/observability"
	rt "porter/internal/runtime"
	"porter/internal/secretbox"
	"porter/internal/sshgw"
	"porter/internal/startup"
	"porter/internal/store"
	portertls "porter/internal/tls"
	"porter/internal/types"
	"porter/internal/volumes"
)

// ----------------------------------------------------------------------
// Server subcommand
// ----------------------------------------------------------------------

func Run(args []string, version string) int {
	bootedAt := time.Now()
	configPath := envOr("PORTER_CONFIG", "porter.toml")

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	logger := logging.Configure(logging.Options{Enabled: cfg.DevEnabled, Level: cfg.LogLevel, RequestLogging: cfg.RequestLogging, IncludeAPIError: cfg.IncludeAPIErrors})
	traceShutdown, traceErr := observability.InitTracing(context.Background(), cfg.OTelEnabled, cfg.OTelServiceName)
	if traceErr != nil {
		log.Printf("observability: OpenTelemetry disabled: %v", traceErr)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := traceShutdown(ctx); err != nil {
				log.Printf("observability: OpenTelemetry shutdown: %v", err)
			}
		}()
	}
	sentryCleanup, sentryErr := observability.InitSentry(cfg.SentryEnabled, cfg.SentryDSN, cfg.SentryEnvironment, version)
	if sentryErr != nil {
		log.Printf("observability: Sentry disabled: %v", sentryErr)
	} else {
		defer sentryCleanup()
	}
	if cfg.DevEnabled {
		logger.Info("development observability enabled", "log_level", cfg.LogLevel, "request_logging", cfg.RequestLogging, "api_errors", cfg.IncludeAPIErrors, "otel", cfg.OTelEnabled, "metrics", cfg.MetricsEnabled, "sentry", cfg.SentryEnabled && cfg.SentryDSN != "")
	}

	// Startup sanity check: report direct Firecracker prerequisites before the
	// first VM boot instead of hiding host failures behind an API request.
	for _, c := range startup.Check(cfg) {
		status := "OK  "
		if !c.OK {
			status = "FAIL"
		}
		log.Printf("startup: [%s] %-18s %s", status, c.Name, c.Message)
		if !c.OK && c.Fatal {
			log.Fatalf("startup: fatal prerequisite missing: %s (%s)", c.Name, c.Message)
		}
	}

	st := store.NewStore(cfg.DatabaseURL)
	defer st.Close()
	if err := st.EnsureSeededAdmin(cfg.BootstrapAdminPassword); err != nil {
		log.Fatalf("auth bootstrap error: %v", err)
	}

	// Optional Redis read-through cache for hot read paths (config [cache]).
	if cfg.CacheEnabled && cfg.CacheURL != "" {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		rc, err := cache.Open(cctx, cfg.CacheURL)
		ccancel()
		if err != nil {
			log.Printf("cache: Redis disabled (%v)", err)
		} else {
			st.SetCache(rc)
			defer rc.Close()
			log.Printf("cache: Redis read-through cache enabled (%s)", cfg.CacheURL)
		}
	}
	hub := event.NewHub()

	vmm := rt.NewEngine(rt.FCConfig{
		Mode:           rt.Mode(cfg.RuntimeMode),
		FirecrackerBin: cfg.FirecrackerBin,
		KernelImage:    cfg.KernelImage,
		RootfsPath:     cfg.RootfsPath,
		SocketDir:      cfg.FirecrackerSocketDir,
		SnapshotDir:    cfg.SnapshotDir,
		LogsDir:        cfg.LogsDir,
		JailerBin:      cfg.JailerBin,
		EnableJailer:   cfg.JailerEnabled,
		CgroupVersion:  cfg.CgroupVersion,
		BalloonMiB:     cfg.BalloonMiB,
		VsockUDSPath:   cfg.VsockPath,
		UseNetwork30:   cfg.NetworkUse30,
	}, st, hub)
	defer vmm.Close()
	vmm.Use30 = cfg.NetworkUse30
	// G4 secrets: let the runtime decrypt project secrets at boot for MMDS
	// injection (values stay out of logs/events; see secretbox pkg).
	if cfg.SecretKey != "" {
		vmm.Rt.SetSecretKey(secretbox.Key(cfg.SecretKey))
	}

	// engineRunner adapts rt.Engine.Snapshot (runtime.SnapshotResult) to the
	// api.VMRunner surface (api.SnapshotInfo). The conversion lives here —
	// not in internal/runtime — because internal/api already depends on
	// internal/runtime via internal/controller, so the reverse import would
	// be a cycle.
	runner := engineRunner{vmm}

	// Controllers own the lifecycle loop (task T7): desired state in the store
	// converges through Deployment/Replica/Node reconcilers. runtime.Engine
	// remains the executor the ReplicaController drives (T7a) via its Rt
	// manager; the API keeps the engine as the imperative escape hatch for
	// direct start/stop actions.
	ctrlMgr := controller.NewManager(30 * time.Second)
	ctrlMgr.Register(controller.NewDeploymentController(st, 30*time.Second))
	ctrlMgr.Register(controller.NewReplicaController(st, vmm.Rt, hub, 30*time.Second))
	ctrlMgr.Register(controller.NewNodeController(st, 30*time.Second))
	// OCM-06: host reconciler ticks node liveness (stale-agent silence) plus
	// stuck-operation lease expiry on the same loop.
	ctrlMgr.Register(controller.NewHostController(st, 180*time.Second, 30*time.Minute))
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	// Tracked goroutines for everything that rides runCtx without a Stop
	// method (meter pump, fc collector, health watchers, sshgw listener):
	// shutdown cancels the context and then joins them with a bounded wait
	// instead of letting cancellation be the only handle (missing-doc
	// shutdown-joins item).
	watchers := newWatchGroup()
	ctrlMgr.Start(runCtx)
	defer ctrlMgr.Stop()
	log.Printf("controllers: deployment, replica, node and host reconcilers started (30s interval)")

	// Task runner: executes QUEUED config-push (compare-and-swap apply),
	// ephemeral (provision → boot → exec → destroy), migrate (cold move),
	// and kernel-build (build.sh) intents from the durable ledger.
	taskRunner := controller.NewTaskRunner(st, &controller.ProvisionerHost{
		VM:    vmm.Rt,
		Stage: controller.DirStager{Dir: cfg.GuestBasesDir},
	}, 15*time.Second)
	taskRunner.Tracer = store.NewPGTraceStore(st)
	taskRunner.Kernel = &kernel.ScriptBuilder{
		Script: "scripts/kernel/build.sh",
		OutDir: "/var/lib/porter/kernels",
	}
	// Scheduled backups: snapshot through the live runtime manager.
	taskRunner.SnapshotVM = func(ctx context.Context, vmID string) (string, string, int64, error) {
		vm, ok := st.GetVM(vmID)
		if !ok {
			return "", "", 0, fmt.Errorf("backup: vm %s not found", vmID)
		}
		res, err := vmm.Snapshot(ctx, vm)
		if err != nil {
			return "", "", 0, err
		}
		var size int64
		for _, p := range []string{res.SnapshotPath, res.MemoryPath} {
			if fi, serr := os.Stat(p); serr == nil {
				size += fi.Size()
			}
		}
		return res.SnapshotPath, res.MemoryPath, size, nil
	}
	taskRunner.Start(runCtx)
	defer taskRunner.Stop()
	log.Printf("task runner started (15s interval)")

	// Reconcile stale VMs from the previous process. Restore durable snapshots
	// through the official Firecracker Unix-socket API when available; VMs that
	// have never been snapshotted remain failed and require an explicit start.
	for _, vm := range st.ListVMs() {
		if vm.State != types.StateBooting && vm.State != types.StateRunning {
			continue
		}
		if vm.SnapshotStatus == "ready" && vm.SnapshotPath != "" && vm.SnapshotMemPath != "" {
			vm.SnapshotStatus = "restoring"
			st.PutVM(vm)
			if err := vmm.Restore(context.Background(), vm); err == nil {
				vm.SnapshotStatus = "ready"
				vm.SnapshotError = ""
				vm.Crashed = false
				st.PutVM(vm)
				continue
			} else {
				vm.SnapshotError = err.Error()
			}
		}
		vm.State = types.StateFailed
		vm.Crashed = true
		if vm.Error == "" {
			vm.Error = "host restarted while this VM was up and no usable snapshot was available"
		}
		st.PutVM(vm)
	}

	netMgr := netmgr.NewNetManager()
	catalog := imagecatalog.New(cfg.ImagesDir)
	if cfg.BaseImageRef != "" {
		baseName := strings.TrimPrefix(cfg.BaseImageRef, "base://")
		if baseName == "" {
			baseName = "default"
		}
		base := imagecatalog.ManifestFromArtifacts(baseName, cfg.BaseImageRef, "Configured Porter base microVM image", "base", cfg.RootfsPath, cfg.KernelImage, 1, 256)
		if err := st.PutGoldenImage(base); err != nil {
			log.Fatalf("base image registration error: %v", err)
		}
	}
	a := api.NewAPI(st, hub, runner, netMgr, catalog, cfg.SecretKey, cfg.BaseDomain, version)
	a.SetJWTKey(auth.KeyPairFromSecret(cfg.SecretKey, "porter"))
	a.SetCustomImagesDir(cfg.CustomImagesDir)

	// Upstream image catalog: Porter ships no guest images, so the deployable
	// set comes from a JSON document in a separate repository. It is fetched
	// lazily and cached, so publishing a new image upstream is an edit to that
	// document and nothing else.
	remoteCat := imagecatalog.NewRemote(cfg.CatalogURL,
		time.Duration(cfg.CatalogTTLHours)*time.Hour, cfg.ImagesDir, nil)
	a.SetRemoteCatalog(remoteCat)
	if remoteCat.Enabled() {
		log.Printf("imagecatalog: remote catalog %s (ttl %dh, auto_pull=%t)",
			cfg.CatalogURL, cfg.CatalogTTLHours, cfg.CatalogAutoPull)
	}
	a.SetHostConfig(cfg)
	a.SetRateLimit(cfg.RateLimitPerMin)

	// Domain auto-assignment: create preview/prod domains for new projects.
	domainMgr := dns.NewDomainManager(st, cfg.BaseDomain, cfg.GatewayIP)
	a.SetDomainManager(domainMgr)

	// Real persistent volumes: host dirs + sparse backing images under volumes/.
	volMgr := volumes.NewManager(cfg.VolumesDir)
	_ = volMgr.EnsureRoot()
	a.SetVolumesManager(volMgr)

	// Infrastructure-VM provisioner for isolated builds + ephemeral runs:
	// the live runtime manager (satisfies the provisioner surface, so
	// buildkit/remote.go stops being dead code).
	a.SetVMProvisioner(vmm.Rt)

	// SMTP email notifications for alerts/events ([notify] config).
	a.SetMailer(notify.New(notify.SMTPConfig{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort,
		User: cfg.SMTPUser, Password: cfg.SMTPPassword,
		From: cfg.SMTPFrom, DefaultTo: cfg.NotifyDefaultTo,
		Enabled: cfg.NotifyEnabled,
	}))

	// Cron scheduler: fires active crons on their 5-field schedule by booting
	// short-lived job microVMs through the same runtime as deploys.
	cronRunner := cronrunner.NewRunner(st, vmm, 30*time.Second)
	cronRunner.Start()
	defer cronRunner.Stop()

	// Metrics collector: samples CPU/memory for running VMs on an interval.
	metricsC := metrics.New(st, 30*time.Second)
	metricsC.Start()
	defer metricsC.Stop()

	// Meter pump: host cgroup samples → durable usage meters (cpu/mem/disk/
	// net per VM, idempotency-keyed). Best-effort: hosts without porter
	// cgroup scopes skip per tick, loudly in logs, never fabricated.
	meterPump := metrics.Pump{
		Sample: metrics.CgroupSampler{Base: "/sys/fs/cgroup/system.slice"},
		Sink:   st,
		ProjectFor: func(vmID string) string {
			if vm, ok := st.GetVM(vmID); ok && vm != nil {
				return vm.ProjectID
			}
			return ""
		},
	}
	watchers.spawn(runCtx, "meter-pump", func(ctx context.Context) {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ids := []string{}
				for _, vm := range st.ListVMs() {
					if vm != nil && vm.State == types.StateRunning {
						ids = append(ids, vm.ID)
					}
				}
				meterPump.PumpOnce(ids, time.Now())
			}
		}
	})

	// Firecracker log/metrics consumer (manual §8): tails per-VM FC logs into
	// store rings + heartbeat metrics every 60s (best-effort, no-op without files).
	fcCollector := rt.NewCollector(st, cfg.LogsDir)
	watchers.spawn(runCtx, "fc-collector", func(ctx context.Context) {
		fcCollector.Start(ctx, 60*time.Second, func() []string {
			ids := []string{}
			for _, vm := range st.ListVMs() {
				ids = append(ids, vm.ID)
			}
			return ids
		})
	})

	// Horizontal autoscaler: adjusts replica pools per AutoscalePolicy.
	if cfg.AutoscaleEnabled {
		a.StartAutoscaler(30 * time.Second)
		defer a.StopAutoscaler()
	}

	// Gateway: host-routing reverse proxy + live traffic logger on its own
	// listener, so the control plane (:8080) and the traffic-facing port stay
	// separate (Vercel-style: gateway faces *.local / project domains).
	// Hoisted so the shutdown path below can drain it with a bounded join.
	var gsrv *http.Server
	if cfg.GatewayEnabled {
		gw := gateway.NewGateway(st)
		if cfg.DNSEnabled {
			gw.SetDNS(dns.New(st))
		}
		// Scale-to-zero wake: a request for a project with no healthy VMs
		// boots one back up (snapshot restore preferred, fresh boot
		// fallback — see api.WakeProject). Boot races the 503+Retry-After
		// response, it never blocks it.
		gw.Waker = func(ctx context.Context, projectID string) error {
			return api.WakeProject(ctx, st, runner, projectID)
		}
		gsrv = &http.Server{
			Addr:              cfg.GatewayListenAddr,
			Handler:           gw,
			ReadHeaderTimeout: 15 * time.Second,
		}
		go func() {
			log.Printf("gateway: host-routing proxy listening on %s (control plane on %s)", cfg.GatewayListenAddr, cfg.ListenAddr)
			if err := gsrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				// Optional listener: a privileged-port denial must degrade the
				// traffic plane, never kill the control plane (deploy fix:
				// non-root hosts stay up serving the API + dashboard).
				log.Printf("gateway server error (traffic plane degraded, control plane up): %v", err)
			}
		}()
	}

	// Host-port forwarder: binds declared HostPorts (compose "8080:80") on the
	// host and proxies them to the running VM's container port. Independent of
	// the HTTP gateway — raw TCP forwarding for non-HTTP/protocol workloads.
	if cfg.GatewayEnabled {
		pf := gateway.NewPortForwarder(st)
		pf.Start()
		defer pf.Close()
		log.Printf("portforward: host-port forwarder started (binds compose HostPorts)")
	}

	// DNS server: authoritative resolver for *.baseDomain zones.
	// Listens on UDP/TCP port 53 and resolves queries to gateway IP.
	if cfg.DNSEnabled && cfg.BaseDomain != "" {
		gwIP := net.ParseIP(cfg.GatewayIP)
		if gwIP == nil {
			gwIP = net.ParseIP("127.0.0.1") // fallback
		}
		dnsSrv := dns.NewServer(st, cfg.BaseDomain, gwIP)
		if err := dnsSrv.Start(":53"); err != nil {
			log.Printf("dns: failed to start on :53: %v (may need root/cap_net_bind)", err)
		} else {
			log.Printf("dns: authoritative server started for *.%s", cfg.BaseDomain)
			defer dnsSrv.Shutdown()
		}
	}

	// TLS: automatic certificate management via Let's Encrypt ACME.
	// Certificates are cached on disk under certs/ (autocert.DirCache) and
	// renew automatically on demand.
	var tlsMgr *portertls.Manager
	if cfg.TLSEnabled && cfg.BaseDomain != "" && cfg.ACMEEmail != "" {
		tlsMgr = portertls.NewManager(cfg.BaseDomain, cfg.ACMEEmail, "certs")
		log.Printf("tls: ACME certificates enabled for *.%s (email: %s)", cfg.BaseDomain, cfg.ACMEEmail)
	}

	// Health checker: watch running VMs that declare a healthcheck and
	// auto-replace unhealthy ones via the VM manager. Watchers ride runCtx so
	// shutdown cancels them; the watchers group joins them with a bounded
	// wait at exit.
	if cfg.HealthEnabled {
		for _, vm := range st.ListVMs() {
			if vm.State != types.StateRunning || vm.Healthcheck == nil {
				continue
			}
			hc := vm.Healthcheck
			ck := health.New(st, hub, func(ctx context.Context, vmID string) {
				v, ok := st.GetVM(vmID)
				if !ok {
					return
				}
				if err := vmm.Restart(ctx, v); err != nil {
					log.Printf("health: replace vm %s: %v", vmID, err)
				}
			})
			watchers.spawn(runCtx, "health/"+vm.ID, func(ctx context.Context) {
				ck.Watch(ctx, vm.ID, health.HealthSpec{
					Type:        hc.Type,
					Path:        hc.Path,
					Port:        hc.Port,
					IntervalSec: hc.IntervalSec,
				})
			})
			log.Printf("health: watching vm %s (%s)", vm.ID, vm.ServiceName)
		}
	}

	// SSH gateway remains opt-in. Direct Firecracker VMs currently expose no
	// task.Exec bridge; a future guest-vsock agent can back this interface.
	if cfg.SSHEnabled {
		sg, err := sshgw.New(sshgw.Config{
			ListenAddr: cfg.SSHListenAddr,
			DataDir:    filepath.Join(cfg.LogsDir, "ssh"),
		}, vmm)
		if err != nil {
			log.Fatalf("[ssh] gateway init: %v", err)
		}
		watchers.spawn(runCtx, "sshgw", func(ctx context.Context) {
			log.Printf("sshgw: SSH gateway listening on %s", cfg.SSHListenAddr)
			// runCtx (not Background) so shutdown closes the listener via
			// the ctx-done path in ListenAndServe; no Stop method exists.
			if err := sg.ListenAndServe(ctx); err != nil {
				log.Printf("sshgw: %v", err)
			}
		})
	}

	// Control API is exposed under the /api/v1/ prefix. The API registers bare
	// paths (e.g. /auth/login) on its own mux; a StripPrefix wrapper maps
	// /api/v1/auth/login -> /auth/login so the route table and tests stay clean.
	const apiPrefix = "/api/v1/"
	mux := http.NewServeMux()
	apiMux := http.NewServeMux()
	a.Routes(apiMux)
	mux.Handle(apiPrefix, http.StripPrefix(strings.TrimRight(apiPrefix, "/"), apiMux))
	// Path-based Vue SPA (createWebHistory): unknown non-API paths fall back
	// to index.html so refresh/deep-links work; missing asset files stay 404.
	api.MountDashboard(mux)

	var handler http.Handler = mux
	if cfg.MetricsEnabled {
		telemetry := observability.NewMetrics()
		// PG-backed gauges: per-VM latest readings + fleet totals, refreshed
		// on every scrape (bounded series; hook recovers to last good).
		telemetry.SetSnapshotHook(func() observability.Snapshot {
			return api.PGGaugeSnapshot(st, volMgr, version)
		})
		mux.HandleFunc("/metrics", telemetry.Handler)
		handler = telemetry.Middleware(handler)
		log.Printf("metrics: Prometheus endpoint enabled at /metrics")
	}
	if cfg.OTelEnabled {
		handler = observability.HTTPHandler(handler)
	}
	handler = logging.Middleware(logger, cfg.DevEnabled && cfg.RequestLogging, handler)
	if cfg.SentryEnabled && cfg.SentryDSN != "" {
		handler = observability.SentryHandler(handler)
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	// Enable TLS if configured.
	if tlsMgr != nil {
		srv.TLSConfig = tlsMgr.GetTLSConfig()
		// Wrap mux with ACME HTTP challenge handler
		srv.Handler = tlsMgr.HTTPHandler(handler)

		log.Printf("Porter %s — Control API listening on %s (HTTPS enabled)", version, cfg.ListenAddr)
	} else {
		log.Printf("Porter %s — Control API listening on %s", version, cfg.ListenAddr)
	}
	dashboardURL := cfg.ListenAddr
	if strings.HasPrefix(dashboardURL, ":") {
		dashboardURL = "127.0.0.1" + dashboardURL
	} else if strings.HasPrefix(dashboardURL, "0.0.0.0:") {
		dashboardURL = "127.0.0.1:" + strings.TrimPrefix(dashboardURL, "0.0.0.0:")
	}
	if !strings.HasPrefix(dashboardURL, "http://") && !strings.HasPrefix(dashboardURL, "https://") {
		dashboardURL = "http://" + dashboardURL
	}
	log.Printf("Dashboard: %s", dashboardURL)
	log.Printf("Database: %s  Config: %s", cfg.DatabaseURL, configPath)
	st.AppendDaemonLog(fmt.Sprintf("=== Porter %s started  pid=%d  %s ===", version, os.Getpid(), dashboardURL))
	log.Printf("runtime: direct Firecracker over per-VM Unix sockets in %s", cfg.FirecrackerSocketDir)
	// Startup gauges: process start + time-to-ready for /metrics + alerts.
	observability.SetStartInfo(bootedAt.Unix(), time.Since(bootedAt).Seconds())

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		if srv.TLSConfig != nil {
			// Use ListenAndServeTLS with empty cert/key since autocert handles them
			serverErr <- srv.ListenAndServeTLS("", "")
		} else {
			serverErr <- srv.ListenAndServe()
		}
	}()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	case sig := <-shutdown:
		log.Printf("Received %s, shutting down...", sig)
	}

	// Cancel the run context up front so the runCtx-riding watchers (health,
	// sshgw listener, meter pump, fc collector) tear down while the HTTP
	// planes drain below; the deferred runCancel stays as a no-op backstop.
	runCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	// Bounded join for the traffic plane: drain the gateway listener (nil
	// when disabled) on its own timeout so a hung keep-alive there never
	// blocks control-plane shutdown.
	if gsrv != nil {
		gctx, gcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := gsrv.Shutdown(gctx); err != nil && err != http.ErrServerClosed {
			log.Printf("gateway shutdown error: %v", err)
		}
		gcancel()
	}
	// Bounded join for the watcher goroutines: they stop via runCtx
	// cancellation (no Stop methods), so give them the same 10s budget the
	// control-plane drain gets and name any that overshoot in the log.
	watchers.join(10 * time.Second)
	log.Printf("Shutdown complete")
	st.AppendDaemonLog(fmt.Sprintf("=== Porter %s stopped ===", version))
	return 0
}

// ----------------------------------------------------------------------
// Edge adapters (internal/server owns the seams between internal packages)
// ----------------------------------------------------------------------

// engineRunner adapts *rt.Engine to api.VMRunner. Everything matches except
// Snapshot: the engine returns runtime.SnapshotResult while the API names
// api.SnapshotInfo (same fields; separate types to avoid an import cycle).
type engineRunner struct {
	*rt.Engine
}

// Snapshot converts the runtime snapshot result to the API's shape.
func (e engineRunner) Snapshot(ctx context.Context, vm *types.VM) (api.SnapshotInfo, error) {
	r, err := e.Engine.Snapshot(ctx, vm)
	if err != nil {
		return api.SnapshotInfo{}, err
	}
	return api.SnapshotInfo{
		SnapshotPath: r.SnapshotPath,
		MemoryPath:   r.MemoryPath,
		CreatedAt:    r.CreatedAt,
	}, nil
}

// spaFileServer delegates to the embedded-dashboard handler in internal/api.
// It exists so the cmd-level SPA regression test (main_test.go) exercises
// the exact handler the server mounts, without duplicating fallback logic.
func spaFileServer(files http.FileSystem) http.Handler {
	return api.SpaFileServer(files)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ----------------------------------------------------------------------
// Watcher join (background goroutines without a Stop method)
// ----------------------------------------------------------------------

// watchGroup tracks the background goroutines that ride runCtx (health
// watchers, sshgw listener, meter pump, fc collector) so shutdown can join
// them with a bounded wait: cancellation is their only stop signal, but it
// no longer has to be the only lifecycle handle. Names are log labels;
// duplicates only blur the late-exit report, never the join itself.
type watchGroup struct {
	wg   sync.WaitGroup
	mu   sync.Mutex
	live map[string]bool
}

func newWatchGroup() *watchGroup {
	return &watchGroup{live: map[string]bool{}}
}

// spawn runs fn on its own goroutine and tracks it under name until it
// returns.
func (g *watchGroup) spawn(ctx context.Context, name string, fn func(context.Context)) {
	g.mu.Lock()
	g.live[name] = true
	g.mu.Unlock()
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() {
			g.mu.Lock()
			delete(g.live, name)
			g.mu.Unlock()
		}()
		fn(ctx)
	}()
}

// join waits up to timeout for every spawned watcher to exit, logging any
// that overshoot the budget. Returns true when all exited in time.
func (g *watchGroup) join(timeout time.Duration) bool {
	waitDone := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(waitDone)
	}()
	deadline := time.After(timeout)
	for {
		select {
		case <-waitDone:
			return true
		case <-deadline:
			g.mu.Lock()
			laggards := make([]string, 0, len(g.live))
			for name := range g.live {
				laggards = append(laggards, name)
			}
			g.mu.Unlock()
			for _, name := range laggards {
				log.Printf("shutdown: watcher %q did not exit within %s", name, timeout)
			}
			return false
		}
	}
}
