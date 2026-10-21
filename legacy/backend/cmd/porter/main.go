//go:build go1.16

// Porter - single binary, three modes, one file.
// Modes live in internal packages; this file only dispatches:
// server (internal/server) + agent (internal/agent) + ptyd (internal/ptyd),
// plus migrate/version/help utilities.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"porter/internal/agent"
	"porter/internal/config"
	"porter/internal/ptyd"
	"porter/internal/server"
	"porter/internal/store"
)

// Version is overridden at build time with -ldflags "-X main.Version=...".
// This default tracks the release this tree represents.
var Version = "v0.0.1-alpha"

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Println(Version)
			return
		}
	}
	// Dashboard-only: `porter` (no subcommand) runs the app. The only other
	// entrypoints are the sibling modes and utilities below.
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(server.Run(args, Version))
	}
	switch args[0] {
	case "server":
		os.Exit(server.Run(args[1:], Version)) // tolerate the old spelling
	case "agent":
		os.Exit(agent.Run(Version))
	case "ptyd":
		os.Exit(ptyd.Run())
	case "migrate":
		os.Exit(runMigrate())
	case "kernel":
		os.Exit(runKernel(args[1:]))
	case "image":
		os.Exit(runImage(args[1:]))
	case "version":
		fmt.Printf("Porter %s\n", Version)
		return
	case "help", "-h", "--help":
		printUsage()
		return
	default:
		os.Exit(server.Run(args, Version))
	}
}

// runMigrate applies all pending migrations, then seeds the minimum default
// data (the config-admin's default org) so the control plane is immediately
// usable. It does NOT start any VM engine or the HTTP listener.
func runMigrate() int {
	configPath := getenv("PORTER_CONFIG", "porter.toml")
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	st := store.NewStore(cfg.DatabaseURL) // NewStore runs all pending Migrate calls
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := st.EnsureDefaultOrg(ctx, "admin", "default"); err != nil {
		log.Fatalf("migrate: seed default org: %v", err)
	}
	log.Printf("migrate: schema up to date and default org seeded (db=%s)", cfg.DatabaseURL)
	return 0
}

func printUsage() {
	fmt.Print(`Porter - the self-hosted PaaS (Firecracker microVMs)

Run the app (control plane + dashboard):

  porter                      # start the API + embedded lifecycle workers
  porter server               # same as above (old spelling)

Host agent + guest terminal daemon:

  porter agent                # host agent: :9090 control + :9091 loopback proxy
                              # (needs PORTER_ENROLL_TOKEN, PORTER_AGENT_CONTROL_TOKEN,
                              #  PORTER_AGENT_PROXY_TOKEN, PORTER_CONTROL_URL)
  porter ptyd                 # guest terminal daemon on 127.0.0.1:7681
                              # (needs PORTER_PTYD_TOKEN)

Utilities:
  porter migrate               # run pending DB migrations + seed default org
  porter kernel set <url|path> # install the vmlinux kernel used to boot microVMs
  porter image add <name> <rootfs.ext4> <vmlinux>
                               # register a bootable image in the image catalog
  porter version               # print the version
  porter help                  # print this help

Config is read from $PORTER_CONFIG (default: porter.toml).

Everything else — deploy, manage VMs, traffic, teams — happens in the
dashboard at http://localhost:8080.
`)
}
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
