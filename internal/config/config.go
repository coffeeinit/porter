// Package config loads Porter's runtime configuration from a porter.toml
// file with PORTER_* environment-variable overrides layered on top. A
// missing file is not an error — env vars or built-in defaults can carry
// the whole config.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds every setting Porter needs to start: server/network settings,
// Firecracker paths, and a one-time database bootstrap secret.
type Config struct {
	ListenAddr string
	BaseDomain string
	SecretKey  string // environment-only key material for project-secret encryption

	// PostgreSQL state store. DatabaseURL is required — LoadConfig refuses to
	// start without it. AutoMigrate runs pending SQL migrations at startup.
	DatabaseURL string
	AutoMigrate bool

	// Linux-host VM wiring for direct Firecracker. Each VM receives a kernel,
	// rootfs.ext4, TAP device, and private API Unix socket.
	RuntimeMode          string // direct only
	BaseImageRef         string // canonical default, e.g. base://default
	FirecrackerSocketDir string // per-VM API sockets, e.g. /run/porter/firecracker
	SnapshotDir          string // durable full snapshot files, e.g. /var/lib/porter/snapshots
	LogsDir              string // per-VM Firecracker stdout/stderr logs

	KernelImage    string // shared vmlinux (provision with `porter kernel set`)
	RootfsPath     string
	FirecrackerBin string
	JailerBin      string // optional: jailer binary for the startup sanity check

	ImagesDir string // directory of data/images/*.json image-catalog manifests (outside backend/)

	// Remote image catalog. Porter ships no guest images: ImagesDir is a local
	// cache and the set of deployable images comes from a small JSON document
	// published in a separate repository (usually GitHub raw). Updating an
	// image means editing that JSON. Empty CatalogURL disables remote images,
	// leaving only locally registered ones.
	CatalogURL      string // e.g. https://raw.githubusercontent.com/<owner>/porter-images/main/catalog.json
	CatalogTTLHours int    // how long a fetched catalog stays fresh (default 24)
	CatalogAutoPull bool   // download artifacts during boot instead of requiring an explicit pull

	// Direct-Firecracker (bare microVM) settings. CustomImagesDir is where
	// user-uploaded .zip microVM images (rootfs.ext4 + vmlinux) are unpacked.
	CustomImagesDir  string // e.g. "../data/custom" (outside backend/)
	GuestBaseDefault string // alpine | debian | ubuntu | custom
	GuestBasesDir    string // managed base manifests and rootfs artifacts
	BuildKitAddr     string // BuildKit Unix socket or tcp endpoint
	BuildKitBin      string // optional buildctl binary path
	GuestAgentPath   string // Porter guest agent installed into managed bases

	// RateLimitPerMin caps control-plane requests per client IP per minute
	// (0 disables). Applied to auth'd routes as a token bucket.
	RateLimitPerMin int

	// Optional control-plane services wired into `porter server`. All default
	// to off so a bare bootstrap keeps working with zero extra listeners.
	GatewayEnabled    bool   // host-routing reverse proxy + traffic logger
	GatewayListenAddr string // e.g. ":80" — faces *.local / project domains
	DNSEnabled        bool   // resolve <svc>.<project>.local for the gateway
	HealthEnabled     bool   // healthcheck + auto-replace of unhealthy VMs
	SSHEnabled        bool   // SSH gateway (needs a task.Exec bridge; off by default)
	SSHListenAddr     string

	// Optional Redis read-through cache. Disabled by default — every cache
	// call is a no-op and the store never touches Redis. When enabled, hot
	// read paths (projects, VMs/replicas, orgs, domains, deployments, builds,
	// image library) are served from Redis for CacheTTLSeconds before falling
	// through to Postgres.
	CacheEnabled    bool
	CacheURL        string // e.g. redis://localhost:6379/0
	CacheTTLSeconds int    // seconds cached reads stay fresh (<=0 keeps default)

	// TLS/ACME: automatic Let's Encrypt certificates.
	TLSEnabled bool   // enable HTTPS via ACME
	ACMEEmail  string // email for Let's Encrypt account
	GatewayIP  string // IP for DNS A records (e.g., public IP of this host)

	// VolumesDir is where real persistent volume data lives (one dir per volume
	// with a sparse data.img of the requested size). Defaults to ../data/volumes
	// (outside backend/, relative to the backend/ working directory).
	VolumesDir string

	// AutoscaleEnabled turns on the horizontal autoscaler (polls project load
	// and adjusts replica pools per each project's AutoscalePolicy).
	AutoscaleEnabled bool

	// SMTP email notifications ([notify]) for alerts and operational events.
	NotifyEnabled   bool   // master switch; when false Send is a no-op
	SMTPHost        string // e.g. smtp.example.com
	SMTPPort        int    // e.g. 587
	SMTPUser        string
	SMTPPassword    string
	SMTPFrom        string // From address
	NotifyDefaultTo string // recipients when an alert has no destination

	// BootstrapAdminPassword is consumed only when the migration-seeded admin
	// row has no password hash. It is never written to TOML or used as a
	// privileged authorization fallback.
	BootstrapAdminPassword string

	// Network layout: false = per-project /24 (default, current guests);
	// true = Firecracker-reference /30 per link (needs updated guest images).
	NetworkUse30 bool
	// Hardening (firecracker-manual §3): jailer + cgroup + balloon + vsock.
	JailerEnabled bool
	CgroupVersion string // v1 | v2
	BalloonMiB    int    // 0 = no balloon device
	VsockPath     string // empty = no vsock device (exec falls back to error)
	// Build packs (build-pipeline §5): external binaries; empty = auto PATH.
	NixpacksBin string
	RailpackBin string
	// BuilderVMImage stages the mandatory isolated build environment. Pack
	// steps fail closed when this image or its VM provisioner is unavailable;
	// host BuildKit is not a supported fallback.
	BuilderVMImage string

	// Development observability is opt-in and never changes authorization behavior.
	DevEnabled        bool
	LogLevel          string
	RequestLogging    bool
	IncludeAPIErrors  bool
	MetricsEnabled    bool // bounded Prometheus-compatible metrics endpoint
	OTelEnabled       bool
	OTelServiceName   string
	SentryEnabled     bool
	SentryDSN         string
	SentryEnvironment string
}

// LoadConfig reads the TOML file at path (if present) and layers
// PORTER_* environment variables on top of it. A missing file is not an
// error — env vars (or the built-in defaults below) can carry the whole
// config.
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{
		ListenAddr:           ":8080",
		DatabaseURL:          "postgres://porter:porter@localhost:5432/porter?sslmode=disable",
		AutoMigrate:          true,
		RuntimeMode:          "direct",
		BaseImageRef:         "base://default",
		FirecrackerSocketDir: "/run/porter/firecracker",
		SnapshotDir:          "/var/lib/porter/snapshots",
		LogsDir:              "/var/log/porter",
		ImagesDir:            "../data/images",
		CustomImagesDir:      "../data/custom",
		CatalogTTLHours:      24,
		GuestBaseDefault:     "alpine",
		GuestBasesDir:        "../data/bases",
		BuildKitAddr:         "unix:///run/buildkit/buildkitd.sock",
		BuildKitBin:          "buildctl",
		GuestAgentPath:       "/usr/local/libexec/porter-guest-agent",
		FirecrackerBin:       "firecracker",

		GatewayListenAddr: ":80",
		SSHListenAddr:     ":2222",
		CacheURL:          "redis://localhost:6379/0",
		CacheTTLSeconds:   15,
		VolumesDir:        "../data/volumes",
		SMTPPort:          587,
		LogLevel:          "info",
		OTelServiceName:   "porter-control-plane",
		SentryEnvironment: "development",
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		sections, perr := ParseTOML(string(data))
		if perr != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, perr)
		}
		cfg.ListenAddr = tomlGet(sections, "server", "listen_addr", cfg.ListenAddr)
		cfg.BaseDomain = tomlGet(sections, "server", "base_domain", cfg.BaseDomain)
		cfg.DatabaseURL = tomlGet(sections, "database", "url", cfg.DatabaseURL)
		cfg.AutoMigrate = tomlBool(sections, "database", "auto_migrate", cfg.AutoMigrate)
		cfg.KernelImage = tomlGet(sections, "firecracker", "kernel_image", cfg.KernelImage)
		cfg.RuntimeMode = tomlGet(sections, "firecracker", "runtime_mode", cfg.RuntimeMode)
		cfg.BaseImageRef = tomlGet(sections, "firecracker", "base_image_ref", cfg.BaseImageRef)
		cfg.RootfsPath = tomlGet(sections, "firecracker", "rootfs_path", cfg.RootfsPath)
		cfg.FirecrackerBin = tomlGet(sections, "firecracker", "firecracker_bin", cfg.FirecrackerBin)
		cfg.FirecrackerSocketDir = tomlGet(sections, "firecracker", "api_socket_dir", cfg.FirecrackerSocketDir)
		cfg.SnapshotDir = tomlGet(sections, "firecracker", "snapshot_dir", cfg.SnapshotDir)
		cfg.LogsDir = tomlGet(sections, "firecracker", "logs_dir", cfg.LogsDir)

		cfg.ImagesDir = tomlGet(sections, "firecracker", "images_dir", cfg.ImagesDir)
		cfg.CustomImagesDir = tomlGet(sections, "firecracker", "custom_images_dir", cfg.CustomImagesDir)
		// [images] — the upstream catalog that lists deployable guest images.
		// Porter caches them under ImagesDir; they are fetched, not shipped.
		cfg.CatalogURL = tomlGet(sections, "images", "catalog_url", cfg.CatalogURL)
		cfg.CatalogTTLHours = tomlInt(sections, "images", "catalog_ttl_hours", cfg.CatalogTTLHours)
		cfg.CatalogAutoPull = tomlBool(sections, "images", "catalog_auto_pull", cfg.CatalogAutoPull)
		cfg.GuestBaseDefault = tomlGet(sections, "build", "guest_base_default", cfg.GuestBaseDefault)
		cfg.GuestBasesDir = tomlGet(sections, "build", "guest_bases_dir", cfg.GuestBasesDir)
		cfg.BuildKitAddr = tomlGet(sections, "build", "buildkit_addr", cfg.BuildKitAddr)
		cfg.BuildKitBin = tomlGet(sections, "build", "buildkit_bin", cfg.BuildKitBin)
		cfg.GuestAgentPath = tomlGet(sections, "build", "guest_agent_path", cfg.GuestAgentPath)

		cfg.FirecrackerBin = tomlGet(sections, "firecracker", "firecracker_bin", cfg.FirecrackerBin)
		cfg.JailerBin = tomlGet(sections, "firecracker", "jailer_bin", cfg.JailerBin)
		cfg.NetworkUse30 = tomlBool(sections, "network", "use_30", cfg.NetworkUse30)
		cfg.JailerEnabled = tomlBool(sections, "firecracker", "jailer_enabled", cfg.JailerEnabled)
		cfg.CgroupVersion = tomlGet(sections, "firecracker", "cgroup_version", cfg.CgroupVersion)
		cfg.BalloonMiB = tomlInt(sections, "firecracker", "balloon_mib", cfg.BalloonMiB)
		cfg.VsockPath = tomlGet(sections, "firecracker", "vsock_path", cfg.VsockPath)
		cfg.NixpacksBin = tomlGet(sections, "build", "nixpacks_bin", cfg.NixpacksBin)
		cfg.RailpackBin = tomlGet(sections, "build", "railpack_bin", cfg.RailpackBin)
		cfg.BuilderVMImage = tomlGet(sections, "build", "builder_vm_image", cfg.BuilderVMImage)
		cfg.RateLimitPerMin = tomlInt(sections, "server", "rate_limit_per_min", cfg.RateLimitPerMin)
		cfg.GatewayEnabled = tomlBool(sections, "gateway", "enabled", cfg.GatewayEnabled)
		cfg.GatewayListenAddr = tomlGet(sections, "gateway", "listen_addr", cfg.GatewayListenAddr)
		cfg.DNSEnabled = tomlBool(sections, "dns", "enabled", cfg.DNSEnabled)
		cfg.HealthEnabled = tomlBool(sections, "health", "enabled", cfg.HealthEnabled)
		cfg.SSHEnabled = tomlBool(sections, "ssh", "enabled", cfg.SSHEnabled)
		cfg.SSHListenAddr = tomlGet(sections, "ssh", "listen_addr", cfg.SSHListenAddr)
		cfg.TLSEnabled = tomlBool(sections, "tls", "enabled", cfg.TLSEnabled)
		cfg.ACMEEmail = tomlGet(sections, "tls", "acme_email", cfg.ACMEEmail)
		cfg.GatewayIP = tomlGet(sections, "dns", "gateway_ip", cfg.GatewayIP)
		cfg.VolumesDir = tomlGet(sections, "server", "volumes_dir", cfg.VolumesDir)
		cfg.AutoscaleEnabled = tomlBool(sections, "autoscale", "enabled", cfg.AutoscaleEnabled)
		cfg.CacheEnabled = tomlBool(sections, "cache", "enabled", cfg.CacheEnabled)
		cfg.CacheURL = tomlGet(sections, "cache", "url", cfg.CacheURL)
		cfg.CacheTTLSeconds = tomlInt(sections, "cache", "ttl_seconds", cfg.CacheTTLSeconds)
		cfg.NotifyEnabled = tomlBool(sections, "notify", "enabled", cfg.NotifyEnabled)
		cfg.SMTPHost = tomlGet(sections, "notify", "smtp_host", cfg.SMTPHost)
		cfg.SMTPPort = tomlInt(sections, "notify", "smtp_port", cfg.SMTPPort)
		cfg.SMTPUser = tomlGet(sections, "notify", "smtp_user", cfg.SMTPUser)
		cfg.SMTPPassword = tomlGet(sections, "notify", "smtp_password", cfg.SMTPPassword)
		cfg.SMTPFrom = tomlGet(sections, "notify", "from", cfg.SMTPFrom)
		cfg.NotifyDefaultTo = tomlGet(sections, "notify", "default_to", cfg.NotifyDefaultTo)
		cfg.DevEnabled = tomlBool(sections, "development", "enabled", cfg.DevEnabled)
		cfg.LogLevel = tomlGet(sections, "development", "log_level", cfg.LogLevel)
		cfg.RequestLogging = tomlBool(sections, "development", "request_logging", cfg.RequestLogging)
		cfg.IncludeAPIErrors = tomlBool(sections, "development", "api_errors", cfg.IncludeAPIErrors)
		cfg.MetricsEnabled = tomlBool(sections, "development", "metrics_enabled", cfg.MetricsEnabled)
		cfg.OTelEnabled = tomlBool(sections, "observability", "otel_enabled", cfg.OTelEnabled)
		cfg.OTelServiceName = tomlGet(sections, "observability", "otel_service_name", cfg.OTelServiceName)
		cfg.SentryEnabled = tomlBool(sections, "observability", "sentry_enabled", cfg.SentryEnabled)
		cfg.SentryDSN = tomlGet(sections, "observability", "sentry_dsn", cfg.SentryDSN)
		cfg.SentryEnvironment = tomlGet(sections, "observability", "sentry_environment", cfg.SentryEnvironment)

	case os.IsNotExist(err):
		// No porter.toml — fine, env vars / defaults carry the config.
	default:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Environment variables win when set — handy for secrets injected
	// by systemd/CI rather than written to disk.
	cfg.ListenAddr = envOr("PORTER_LISTEN_ADDR", cfg.ListenAddr)
	cfg.BaseDomain = envOr("PORTER_BASE_DOMAIN", cfg.BaseDomain)
	cfg.SecretKey = envOr("PORTER_SECRET_KEY", cfg.SecretKey)
	cfg.DatabaseURL = envOr("PORTER_DATABASE_URL", cfg.DatabaseURL)
	cfg.AutoMigrate = envBool("PORTER_AUTO_MIGRATE", cfg.AutoMigrate)
	cfg.RuntimeMode = envOr("PORTER_RUNTIME_MODE", cfg.RuntimeMode)
	cfg.BaseImageRef = envOr("PORTER_BASE_IMAGE_REF", cfg.BaseImageRef)
	cfg.FirecrackerSocketDir = envOr("PORTER_FIRECRACKER_API_SOCKET_DIR", cfg.FirecrackerSocketDir)
	cfg.SnapshotDir = envOr("PORTER_FIRECRACKER_SNAPSHOT_DIR", cfg.SnapshotDir)
	cfg.KernelImage = envOr("PORTER_KERNEL_IMAGE", cfg.KernelImage)
	cfg.RootfsPath = envOr("PORTER_ROOTFS_PATH", cfg.RootfsPath)
	cfg.FirecrackerBin = envOr("PORTER_FIRECRACKER_BIN", cfg.FirecrackerBin)
	cfg.LogsDir = envOr("PORTER_LOGS_DIR", cfg.LogsDir)
	cfg.ImagesDir = envOr("PORTER_IMAGES_DIR", cfg.ImagesDir)
	cfg.CustomImagesDir = envOr("PORTER_CUSTOM_IMAGES_DIR", cfg.CustomImagesDir)
	cfg.CatalogURL = envOr("PORTER_CATALOG_URL", cfg.CatalogURL)
	cfg.CatalogTTLHours = envInt("PORTER_CATALOG_TTL_HOURS", cfg.CatalogTTLHours)
	cfg.CatalogAutoPull = envBool("PORTER_CATALOG_AUTO_PULL", cfg.CatalogAutoPull)
	cfg.GuestBaseDefault = envOr("PORTER_GUEST_BASE_DEFAULT", cfg.GuestBaseDefault)
	cfg.GuestBasesDir = envOr("PORTER_GUEST_BASES_DIR", cfg.GuestBasesDir)
	cfg.BuildKitAddr = envOr("PORTER_BUILDKIT_ADDR", cfg.BuildKitAddr)
	cfg.BuildKitBin = envOr("PORTER_BUILDKIT_BIN", cfg.BuildKitBin)
	cfg.BuilderVMImage = envOr("PORTER_BUILDER_VM_IMAGE", cfg.BuilderVMImage)
	cfg.GuestAgentPath = envOr("PORTER_GUEST_AGENT_PATH", cfg.GuestAgentPath)

	cfg.FirecrackerBin = envOr("PORTER_FIRECRACKER_BIN", cfg.FirecrackerBin)
	cfg.JailerBin = envOr("PORTER_JAILER_BIN", cfg.JailerBin)
	cfg.NetworkUse30 = envBool("PORTER_NETWORK_USE_30", cfg.NetworkUse30)
	cfg.JailerEnabled = envBool("PORTER_JAILER_ENABLED", cfg.JailerEnabled)
	cfg.CgroupVersion = envOr("PORTER_CGROUP_VERSION", cfg.CgroupVersion)
	cfg.BalloonMiB = envInt("PORTER_BALLOON_MIB", cfg.BalloonMiB)
	cfg.VsockPath = envOr("PORTER_VSOCK_PATH", cfg.VsockPath)
	cfg.NixpacksBin = envOr("PORTER_NIXPACKS_BIN", cfg.NixpacksBin)
	cfg.RailpackBin = envOr("PORTER_RAILPACK_BIN", cfg.RailpackBin)
	cfg.RateLimitPerMin = envInt("PORTER_RATE_LIMIT_PER_MIN", cfg.RateLimitPerMin)
	cfg.GatewayEnabled = envBool("PORTER_GATEWAY_ENABLED", cfg.GatewayEnabled)
	cfg.GatewayListenAddr = envOr("PORTER_GATEWAY_LISTEN_ADDR", cfg.GatewayListenAddr)
	cfg.DNSEnabled = envBool("PORTER_DNS_ENABLED", cfg.DNSEnabled)
	cfg.HealthEnabled = envBool("PORTER_HEALTH_ENABLED", cfg.HealthEnabled)
	cfg.SSHEnabled = envBool("PORTER_SSH_ENABLED", cfg.SSHEnabled)
	cfg.SSHListenAddr = envOr("PORTER_SSH_LISTEN_ADDR", cfg.SSHListenAddr)
	cfg.BootstrapAdminPassword = envOr("PORTER_BOOTSTRAP_ADMIN_PASSWORD", cfg.BootstrapAdminPassword)
	cfg.TLSEnabled = envBool("PORTER_TLS_ENABLED", cfg.TLSEnabled)
	cfg.ACMEEmail = envOr("PORTER_ACME_EMAIL", cfg.ACMEEmail)
	cfg.GatewayIP = envOr("PORTER_GATEWAY_IP", cfg.GatewayIP)
	cfg.VolumesDir = envOr("PORTER_VOLUMES_DIR", cfg.VolumesDir)
	cfg.AutoscaleEnabled = envBool("PORTER_AUTOSCALE_ENABLED", cfg.AutoscaleEnabled)
	cfg.CacheEnabled = envBool("PORTER_CACHE_ENABLED", cfg.CacheEnabled)
	cfg.CacheURL = envOr("PORTER_CACHE_URL", cfg.CacheURL)
	cfg.CacheTTLSeconds = envInt("PORTER_CACHE_TTL_SECONDS", cfg.CacheTTLSeconds)
	cfg.NotifyEnabled = envBool("PORTER_NOTIFY_ENABLED", cfg.NotifyEnabled)
	cfg.SMTPHost = envOr("PORTER_SMTP_HOST", cfg.SMTPHost)
	cfg.SMTPPort = envInt("PORTER_SMTP_PORT", cfg.SMTPPort)
	cfg.SMTPUser = envOr("PORTER_SMTP_USER", cfg.SMTPUser)
	cfg.SMTPPassword = envOr("PORTER_SMTP_PASSWORD", cfg.SMTPPassword)
	cfg.SMTPFrom = envOr("PORTER_SMTP_FROM", cfg.SMTPFrom)
	cfg.NotifyDefaultTo = envOr("PORTER_NOTIFY_DEFAULT_TO", cfg.NotifyDefaultTo)
	cfg.DevEnabled = envBool("PORTER_DEV_ENABLED", cfg.DevEnabled)
	cfg.LogLevel = envOr("PORTER_LOG_LEVEL", cfg.LogLevel)
	cfg.RequestLogging = envBool("PORTER_REQUEST_LOGGING", cfg.RequestLogging)
	cfg.IncludeAPIErrors = envBool("PORTER_API_ERRORS", cfg.IncludeAPIErrors)
	cfg.MetricsEnabled = envBool("PORTER_METRICS_ENABLED", cfg.MetricsEnabled)
	cfg.OTelEnabled = envBool("PORTER_OTEL_ENABLED", cfg.OTelEnabled)
	cfg.OTelServiceName = envOr("PORTER_OTEL_SERVICE_NAME", cfg.OTelServiceName)
	cfg.SentryEnabled = envBool("PORTER_SENTRY_ENABLED", cfg.SentryEnabled)
	cfg.SentryDSN = envOr("PORTER_SENTRY_DSN", cfg.SentryDSN)
	cfg.SentryEnvironment = envOr("PORTER_SENTRY_ENVIRONMENT", cfg.SentryEnvironment)

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("no database url configured — set [database] url in %s or PORTER_DATABASE_URL", path)
	}
	if _, err := parseRuntimeMode(cfg.RuntimeMode); err != nil {
		return nil, err
	}
	return cfg, nil
}

func parseRuntimeMode(raw string) (string, error) {
	if raw == "" || raw == "direct" {
		return "direct", nil
	}
	return "", fmt.Errorf("invalid firecracker runtime_mode %q (want direct)", raw)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// tomlInt reads a [section] key as an int with a default (0 on parse error).
func tomlInt(sections map[string]map[string]string, section, key string, def int) int {
	v := tomlGet(sections, section, key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envInt parses a PORTER_* int with a default.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envBool parses a PORTER_* boolean (true/1/yes) with a default.
func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

// tomlBool reads a [section] key as a boolean with a default.
func tomlBool(sections map[string]map[string]string, section, key string, def bool) bool {
	v := tomlGet(sections, section, key, "")
	if v == "" {
		return def
	}
	switch v {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}
