package buildkit

import (
	"os"
	"path/filepath"
)

// Engine is the detected build front-end. All engines feed the same OCI
// output (docs/build-pipeline.md §5); runtime stays Firecracker either way.
type Engine string

const (
	EngineDockerfile Engine = "dockerfile"
	EngineNixpacks   Engine = "nixpacks"
	EngineRailpack   Engine = "railpack"
	EngineBuildpacks Engine = "buildpacks"
	EngineStatic     Engine = "static"
)

// Detect inspects contextDir and returns the build engine in priority order:
// explicit config (nixpacks.toml/railpack.json) > Dockerfile > static publish
// dir > ecosystem manifests (nixpacks providers) > buildpacks fallback.
// It never executes anything; Build/EnsureOCI consume the result.
func Detect(contextDir string) Engine {
	has := func(names ...string) bool {
		for _, n := range names {
			if st, err := os.Stat(filepath.Join(contextDir, n)); err == nil && !st.IsDir() {
				return true
			}
		}
		return false
	}
	isDir := func(n string) bool {
		st, err := os.Stat(filepath.Join(contextDir, n))
		return err == nil && st.IsDir()
	}
	switch {
	case has("railpack.json"):
		return EngineRailpack
	case has("nixpacks.toml", ".nixpacks.toml", "NIXPACKS.toml"):
		return EngineNixpacks
	case has("Dockerfile", "dockerfile", "Containerfile"):
		return EngineDockerfile
	case isDir("dist") || isDir("build") || has("index.html"):
		return EngineStatic
	case has("package.json", "requirements.txt", "pyproject.toml", "go.mod", "Cargo.toml", "Gemfile", "composer.json"):
		return EngineNixpacks
	case has("project.toml", "Procfile"):
		return EngineBuildpacks
	default:
		return EngineBuildpacks
	}
}

// Plan is the reproducible build plan auditors/rebuilds consume.
type Plan struct {
	Engine     Engine            `json:"engine"`
	ContextDir string            `json:"context_dir"`
	Dockerfile string            `json:"dockerfile,omitempty"`
	OutputOCI  string            `json:"output_oci"`
	Args       map[string]string `json:"args,omitempty"`    // non-secret --build-arg/--env
	Secrets    map[string]string `json:"secrets,omitempty"` // dockerfile-engine only (memory-only, never stored)
}

// PlanFor builds the plan for one source dir (Dockerfile path resolved only
// for the dockerfile engine; other engines synthesize OCI via their pack).
func PlanFor(contextDir, outputOCI string) Plan {
	eng := Detect(contextDir)
	p := Plan{Engine: eng, ContextDir: contextDir, OutputOCI: outputOCI}
	if eng == EngineDockerfile {
		for _, c := range []string{"Dockerfile", "dockerfile", "Containerfile"} {
			if _, err := os.Stat(filepath.Join(contextDir, c)); err == nil {
				p.Dockerfile = filepath.Join(contextDir, c)
				break
			}
		}
	}
	return p
}
