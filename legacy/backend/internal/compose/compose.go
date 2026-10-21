// Package compose parses a deliberately constrained Compose v3 subset:
// image-based services only (no build:), flat key/value under each
// service, and a topological sort on depends_on. It is a hand-rolled
// indentation parser (no external YAML dependency).
package compose

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"porter/internal/types"
)

// ComposeService is one parsed `services.<name>` entry.
type ComposeService struct {
	Name        string
	Image       string
	Rootfs      string
	Ports       []types.Port
	Env         map[string]string
	Replicas    int
	DependsOn   []string
	Networks    []string // service-level `networks:` membership (shared bridge)
	Healthcheck *types.Healthcheck
	Restart     string
}

// TopLevelNetworks is the set of user-declared networks at the top level
// (`networks:` in the compose file). A service that names one of these shares
// that bridge with every other attached service — Porter's "shared network"
// model: one shared microVM bridge with its own DNS, per-service sandbox.
type TopLevelNetworks struct {
	Names []string
}

// ParseTopLevelNetworks extracts the declared `networks:` block at the top of
// a compose file. It is a light scan (the constrained parser focuses on
// services); unknown mapping bodies are ignored.
func ParseTopLevelNetworks(yamlText string) []string {
	lines := strings.Split(strings.ReplaceAll(yamlText, "\t", "    "), "\n")
	inNetworks := false
	networksIndent := -1
	seen := map[string]bool{}
	var out []string
	for _, raw := range lines {
		line := regexp.MustCompile(`(^|\s)#.*$`).ReplaceAllString(raw, "")
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if !inNetworks {
			if trimmed == "networks:" {
				inNetworks = true
				networksIndent = indent
			}
			continue
		}
		if indent <= networksIndent {
			break
		}
		// A network name at the first level under `networks:` (e.g. `default:`)
		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
			name := strings.TrimSuffix(trimmed, ":")
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// ParseCompose implements the constrained subset described in the README:
// image-based services only (no `build:`), flat key/value under each
// service, and a topological sort on `depends_on`. This is a hand-rolled
// indentation parser (no external YAML dependency).
func ParseCompose(yamlText string) ([]ComposeService, error) {
	lines := strings.Split(strings.ReplaceAll(yamlText, "\t", "    "), "\n")

	servicesIndent := -1
	inServices := false
	var svc *ComposeService
	svcIndent := -1
	section := "" // "ports" | "environment" | "depends_on" | "healthcheck" | ""
	sectionIndent := -1

	services := map[string]*ComposeService{}
	var order []string

	commentRe := regexp.MustCompile(`(^|\s)#.*$`)

	for lineNo, raw := range lines {
		line := commentRe.ReplaceAllString(raw, "")
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)

		if !inServices {
			if trimmed == "services:" {
				inServices = true
				servicesIndent = indent
			}
			continue
		}

		// A new top-level key (e.g. "networks:", "volumes:") ends the services block.
		if indent <= servicesIndent && trimmed != "services:" {
			break
		}

		// New service definition: exactly one indent level under "services:".
		if svcIndent == -1 || indent == svcIndent {
			if !strings.HasSuffix(trimmed, ":") {
				return nil, fmt.Errorf("compose parse error at line %d: expected service name, got %q", lineNo+1, trimmed)
			}
			name := strings.TrimSuffix(trimmed, ":")
			if name == "" {
				return nil, fmt.Errorf("compose parse error at line %d: empty service name", lineNo+1)
			}
			svcIndent = indent
			svc = &ComposeService{Name: name, Replicas: 1, Env: map[string]string{}}
			services[name] = svc
			order = append(order, name)
			section = ""
			sectionIndent = -1
			continue
		}

		if svc == nil {
			return nil, fmt.Errorf("compose parse error at line %d: value outside of a service", lineNo+1)
		}

		// Section list/map items (deeper than the current section header).
		if section != "" && indent > sectionIndent {
			item := strings.TrimPrefix(trimmed, "- ")
			switch section {
			case "ports":
				p, err := parsePort(item)
				if err != nil {
					return nil, fmt.Errorf("compose parse error at line %d: %w", lineNo+1, err)
				}
				svc.Ports = append(svc.Ports, p)
			case "environment":
				k, v, err := parseKV(item)
				if err != nil {
					return nil, fmt.Errorf("compose parse error at line %d: %w", lineNo+1, err)
				}
				svc.Env[k] = v
			case "depends_on":
				svc.DependsOn = append(svc.DependsOn, item)
			case "networks":
				svc.Networks = append(svc.Networks, item)
			case "deploy":
				if k, v, ok := strings.Cut(item, ":"); ok && strings.TrimSpace(k) == "replicas" {
					n, err := strconv.Atoi(strings.TrimSpace(v))
					if err != nil {
						return nil, fmt.Errorf("compose parse error at line %d: replicas must be an integer", lineNo+1)
					}
					svc.Replicas = n
				}
			case "healthcheck":
				k, v, _ := strings.Cut(item, ":")
				k = strings.TrimSpace(k)
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				if svc.Healthcheck == nil {
					svc.Healthcheck = &types.Healthcheck{Type: "tcp"}
				}
				switch k {
				case "test":
					svc.Healthcheck.Type = "http"
				case "interval":
					svc.Healthcheck.IntervalSec = parseDurationSeconds(v)
				}
			}
			continue
		}

		// Service-level key: value (or key: to open a section).
		key, val, _ := strings.Cut(trimmed, ":")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)

		switch key {
		case "image":
			svc.Image = val
		case "restart":
			svc.Restart = val
		case "build":
			return nil, fmt.Errorf(`compose parse error: service %q: only image-based services are supported (no "build:")`, svc.Name)
		case "ports", "environment", "depends_on", "networks", "deploy", "healthcheck":
			section = key
			sectionIndent = indent
		default:
			// unrecognized key: ignored (constrained subset)
		}
	}

	if len(services) == 0 {
		return nil, fmt.Errorf("compose parse error: no services found under `services:`")
	}

	ordered, err := topoSort(services, order)
	if err != nil {
		return nil, err
	}
	for _, s := range ordered {
		if s.Image == "" {
			return nil, fmt.Errorf(`compose parse error: service %q: missing required "image:"`, s.Name)
		}
		if s.Replicas <= 0 {
			s.Replicas = 1
		}
	}
	return ordered, nil
}

func parsePort(item string) (types.Port, error) {
	item = strings.Trim(item, `"'`)
	proto := "tcp"
	if strings.Contains(item, "/") {
		parts := strings.SplitN(item, "/", 2)
		item = parts[0]
		proto = parts[1]
	}
	parts := strings.Split(item, ":")
	portStr := parts[len(parts)-1]
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return types.Port{}, fmt.Errorf("invalid port %q", item)
	}

	hostPort := 0
	if len(parts) > 1 {
		hp, err := strconv.Atoi(parts[0])
		if err == nil {
			hostPort = hp
		}
	}

	return types.Port{ContainerPort: p, HostPort: hostPort, Protocol: proto}, nil
}

func parseKV(item string) (string, string, error) {
	item = strings.Trim(item, `"'`)
	if k, v, ok := strings.Cut(item, "="); ok {
		return strings.TrimSpace(k), strings.TrimSpace(v), nil
	}
	if k, v, ok := strings.Cut(item, ":"); ok {
		return strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`), nil
	}
	return "", "", fmt.Errorf("invalid environment entry %q", item)
}

func parseDurationSeconds(s string) int {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "s") {
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "s"))
		return n
	}
	n, _ := strconv.Atoi(s)
	return n
}

// topoSort resolves depends_on into a boot order, refusing circular deps.
func topoSort(services map[string]*ComposeService, declOrder []string) ([]ComposeService, error) {
	visited := map[string]int{} // 0=unvisited 1=visiting 2=done
	var out []ComposeService

	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		switch visited[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("compose parse error: circular dependency: %s -> %s", strings.Join(path, " -> "), name)
		}
		visited[name] = 1
		s, ok := services[name]
		if !ok {
			return fmt.Errorf("compose parse error: %q depends_on unknown service %q", path[len(path)-1], name)
		}
		deps := append([]string{}, s.DependsOn...)
		sort.Strings(deps)
		for _, d := range deps {
			if err := visit(d, append(path, name)); err != nil {
				return err
			}
		}
		visited[name] = 2
		out = append(out, *s)
		return nil
	}

	for _, name := range declOrder {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
