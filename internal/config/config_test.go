package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigEnvCarriesRequiredSecrets(t *testing.T) {
	t.Setenv("PORTER_BOOTSTRAP_ADMIN_PASSWORD", "")
	t.Setenv("PORTER_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("PORTER_GATEWAY_ENABLED", "true")
	t.Setenv("PORTER_GATEWAY_LISTEN_ADDR", ":8081")

	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "porter.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Fatalf("unexpected listen addr %q", cfg.ListenAddr)
	}
	if !cfg.GatewayEnabled {
		t.Fatal("gateway should be enabled via env")
	}
	if cfg.GatewayListenAddr != ":8081" {
		t.Fatalf("unexpected gateway addr %q", cfg.GatewayListenAddr)
	}
}

func TestLoadConfigDoesNotRequireTomlAdminOrAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porter.toml")
	if err := os.WriteFile(path, []byte("[database]\nurl = \"postgres://u:p@localhost:5432/db\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BootstrapAdminPassword != "" {
		t.Fatal("TOML must not supply a bootstrap admin password")
	}
}

func TestParseTOMLBasic(t *testing.T) {
	text := "[server]\nlisten_addr = \":9999\"\nbase_domain = \"example.com\"\n[database]\nurl = \"postgres://db\"\n[gateway]\nenabled = true\n"
	sections, err := ParseTOML(text)
	if err != nil {
		t.Fatal(err)
	}
	if sections["server"]["listen_addr"] != ":9999" {
		t.Fatalf("unexpected listen_addr %q", sections["server"]["listen_addr"])
	}
	if sections["database"]["url"] != "postgres://db" {
		t.Fatal("database URL not parsed")
	}
	if sections["gateway"]["enabled"] != "true" {
		t.Fatal("gateway enabled not parsed")
	}
}

func TestParseTOMLRejectsBadLine(t *testing.T) {
	if _, err := ParseTOML("[server]\njust-a-key\n"); err == nil {
		t.Fatal("expected parse error for bare line")
	}
}

func TestLoadConfigDirectFirecrackerTOML(t *testing.T) {
	for _, key := range []string{
		"PORTER_BOOTSTRAP_ADMIN_PASSWORD", "PORTER_DATABASE_URL",
		"PORTER_RUNTIME_MODE", "PORTER_FIRECRACKER_API_SOCKET_DIR",
		"PORTER_KERNEL_IMAGE", "PORTER_ROOTFS_PATH", "PORTER_FIRECRACKER_BIN",
	} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "porter.toml")
	contents := `[server]
	[database]
url = "postgres://toml:toml@localhost:5432/porter?sslmode=disable"
auto_migrate = true
[firecracker]
runtime_mode = "direct"
api_socket_dir = "/run/porter/firecracker"
kernel_image = "/var/lib/porter/vmlinux"
rootfs_path = "/var/lib/porter/rootfs.ext4"
firecracker_bin = "/usr/local/bin/firecracker"
logs_dir = "/var/log/porter"
images_dir = "/var/lib/porter/images"
custom_images_dir = "/var/lib/porter/custom"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeMode != "direct" || cfg.FirecrackerSocketDir != "/run/porter/firecracker" {
		t.Fatalf("unexpected direct runtime config: mode=%q socket=%q", cfg.RuntimeMode, cfg.FirecrackerSocketDir)
	}
	if cfg.KernelImage != "/var/lib/porter/vmlinux" || cfg.RootfsPath != "/var/lib/porter/rootfs.ext4" {
		t.Fatalf("unexpected boot artifacts: kernel=%q rootfs=%q", cfg.KernelImage, cfg.RootfsPath)
	}
	if cfg.BootstrapAdminPassword != "" || cfg.DatabaseURL == "" {
		t.Fatal("TOML must not provide admin credentials")
	}
}
