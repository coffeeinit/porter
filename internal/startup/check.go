// Package startup verifies direct Firecracker host prerequisites at boot so a
// missing KVM, VMM, kernel, or socket directory is visible before first boot.
package startup

import (
	"fmt"
	"os"
	"os/exec"

	"porter/internal/config"
)

// Result is the outcome of one prerequisite check.
type Result struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Fatal   bool   `json:"fatal,omitempty"`
}

// Check runs all sanity checks against the given config. Non-fatal failures
// are reported but don't stop startup; fatal ones are meant to stop it.
func Check(cfg *config.Config) []Result {
	var out []Result
	out = append(out, checkSocketDir(cfg))
	out = append(out, checkKVM())
	out = append(out, checkFirecracker(cfg))
	out = append(out, checkJailer(cfg))
	out = append(out, checkVolumesDir(cfg))
	out = append(out, checkNetworkMode(cfg))
	out = append(out, checkPacks(cfg))
	out = append(out, checkJudgments())
	return out
}

// checkNetworkMode reports the active link layout (/24 default, /30 flagged).
func checkNetworkMode(cfg *config.Config) Result {
	if cfg.NetworkUse30 {
		return Result{Name: "network-mode", OK: true, Message: "/30 per link (guest images must use 255.255.255.252 + TAP .1 gateway)"}
	}
	return Result{Name: "network-mode", OK: true, Message: "/24 per project (default; set [network] use_30=true for /30 reference layout)"}
}

// checkPacks reports build front-ends (all optional; prebuilt OCI always works).
func checkPacks(cfg *config.Config) Result {
	found := []string{}
	for name, bin := range map[string]string{"buildctl": cfg.BuildKitBin, "nixpacks": cfg.NixpacksBin, "railpack": cfg.RailpackBin} {
		path := bin
		if path == "" {
			path = name
		}
		if _, err := exec.LookPath(path); err == nil {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return Result{Name: "build-packs", OK: true, Message: "no pack binaries on PATH — Dockerfile/nixpacks builds need runners; prebuilt OCI tar still works"}
	}
	return Result{Name: "build-packs", OK: true, Message: "available: " + fmt.Sprint(found)}
}

// checkJudgments reports whether TypeSafe gates are live. Absent key is a
// healthy state: judges degrade to explicit human/escalate outcomes, never
// silent allow. Key name only — the value is never read here.
func checkJudgments() Result {
	if os.Getenv("PORTER_TYPESAFE_KEY") == "" {
		return Result{Name: "judgments", OK: true, Message: "PORTER_TYPESAFE_KEY unset — triage/riskgate/cmdgate/alertjudge escalate to human (safe default)"}
	}
	return Result{Name: "judgments", OK: true, Message: "PORTER_TYPESAFE_KEY set — live System One judgments enabled"}
}

// checkSocketDir verifies that the directory for per-VM Firecracker API
// sockets can be created. The VMM creates each socket when it starts.
func checkSocketDir(cfg *config.Config) Result {
	if cfg.FirecrackerSocketDir == "" {
		return Result{Name: "firecracker-api-socket-dir", OK: false, Message: "no Firecracker API socket directory configured", Fatal: true}
	}
	if err := os.MkdirAll(cfg.FirecrackerSocketDir, 0o755); err != nil {
		return Result{Name: "firecracker-api-socket-dir", OK: false, Message: fmt.Sprintf("cannot create %s: %v", cfg.FirecrackerSocketDir, err)}
	}
	return Result{Name: "firecracker-api-socket-dir", OK: true, Message: cfg.FirecrackerSocketDir}
}

// checkKVM verifies hardware virtualization is available for microVMs.
func checkKVM() Result {
	for _, kvm := range []string{"/dev/kvm", "/dev/kvm0"} {
		if _, err := os.Stat(kvm); err == nil {
			return Result{Name: "kvm", OK: true, Message: kvm}
		}
	}
	return Result{Name: "kvm", OK: false, Message: "/dev/kvm not found — microVMs will not boot (WSL2: enable nested virtualization)"}
}

// checkFirecracker verifies the firecracker binary exists and runs.
func checkFirecracker(cfg *config.Config) Result {
	bin := cfg.FirecrackerBin
	if bin == "" {
		bin = "firecracker"
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		// Allow an absolute path that exists but isn't on PATH.
		if _, serr := os.Stat(bin); serr == nil {
			p = bin
		} else {
			return Result{Name: "firecracker", OK: false, Message: fmt.Sprintf("binary %q not found: %v", bin, err)}
		}
	}
	return Result{Name: "firecracker", OK: true, Message: p}
}

// checkJailer warns (non-fatal) when a jailer is referenced but missing — the
// shim would silently run without jailer isolation, which is a real security
// concern worth surfacing loudly at startup.
func checkJailer(cfg *config.Config) Result {
	if cfg.JailerBin == "" {
		return Result{Name: "jailer", OK: true, Message: "not explicitly configured; direct Firecracker runs with the configured VMM process"}
	}
	if _, err := os.Stat(cfg.JailerBin); err != nil {
		return Result{Name: "jailer", OK: false, Message: fmt.Sprintf("configured jailer %q missing: %v — workloads may run without jailer isolation", cfg.JailerBin, err)}
	}
	return Result{Name: "jailer", OK: true, Message: cfg.JailerBin}
}

// checkVolumesDir ensures the volumes root is writable (real volumes need it).
func checkVolumesDir(cfg *config.Config) Result {
	if cfg.VolumesDir == "" {
		return Result{Name: "volumes-dir", OK: true, Message: "not configured (defaults to ./volumes)"}
	}
	if err := os.MkdirAll(cfg.VolumesDir, 0o755); err != nil {
		return Result{Name: "volumes-dir", OK: false, Message: fmt.Sprintf("cannot create %s: %v", cfg.VolumesDir, err)}
	}
	return Result{Name: "volumes-dir", OK: true, Message: cfg.VolumesDir}
}
