// One-click services: databases and apps from templates.
// A template is image + env (passwords generated) + ports + volume. The
// service boots through the normal replica path, so an unstaged image fails
// honestly with "image not staged" instead of pretending. Secrets are stored
// encrypted; only names/ports ever leave in responses.
package marketplace

import (
	"fmt"
	"strings"

	"porter/internal/store"
)

// ServiceTemplate is one deployable service definition. Env values may use
// Template placeholders resolved at provision time:
// ${SERVICE_PASSWORD_<NAME>} (generated per install),
// ${SERVICE_USER_<NAME>} (default user for the engine),
// ${SERVICE_FQDN} (the service's first public hostname, when assigned).
type ServiceTemplate struct {
	Name        string            // postgres, redis, mysql, mariadb, mongo, minio
	Image       string            // golden image ref, e.g. custom://postgres-16
	Version     string            // default tag recorded for display
	Ports       []int             // container ports to publish
	VolumeMiB   int               // data volume size
	Env         map[string]string // static env (placeholders allowed)
	Secrets     []string          // env names generated per install
	Healthcheck *ServiceHealth    // container health probe
	Description string
}

// ServiceHealth is a template-level probe rendered onto booted replicas.
type ServiceHealth struct {
	Type        string // tcp | http
	Path        string // http path (http only)
	Port        int
	IntervalSec int
}

// ExpandPlaceholders resolves ${SERVICE_PASSWORD_*}, ${SERVICE_USER_*},
// and ${SERVICE_FQDN} against generated secrets and the assigned hostname.
// Unknown placeholders are left intact (fail visible, never silent empty).
func ExpandPlaceholders(env map[string]string, secrets map[string]string, fqdn string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = expandOne(v, secrets, fqdn)
	}
	return out
}

func expandOne(v string, secrets map[string]string, fqdn string) string {
	for {
		start := strings.Index(v, "${")
		if start < 0 {
			return v
		}
		end := strings.Index(v[start:], "}")
		if end < 0 {
			return v
		}
		end += start
		name := v[start+2 : end]
		repl, ok := "", false
		switch {
		case strings.HasPrefix(name, "SERVICE_PASSWORD_"):
			repl, ok = secrets[strings.TrimPrefix(name, "SERVICE_PASSWORD_")]
			if !ok {
				repl, ok = secrets[name]
			}
		case strings.HasPrefix(name, "SERVICE_USER_"):
			repl, ok = secrets[strings.TrimPrefix(name, "SERVICE_USER_")]
			if !ok {
				repl = defaultServiceUser(name)
				ok = true
			}
		case name == "SERVICE_FQDN":
			repl, ok = fqdn, fqdn != ""
		}
		if !ok {
			return v // unknown: leave intact for the operator to see
		}
		v = v[:start] + repl + v[end+1:]
	}
}

// defaultServiceUser maps engines to their conventional superuser.
// FALLBACK ONLY: engine_defaults (migration 0035) is the source of truth,
// consulted via ResolveTemplate; this map covers ExpandPlaceholders when no
// secret was generated and ResolveTemplate when the DB row is missing.
func defaultServiceUser(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "POSTGRES"):
		return "postgres"
	case strings.Contains(upper, "MYSQL") || strings.Contains(upper, "MARIADB"):
		return "root"
	case strings.Contains(upper, "MONGO"):
		return "root"
	case strings.Contains(upper, "REDIS"):
		return "default"
	case strings.Contains(upper, "MINIO"):
		return "porter"
	default:
		return "admin"
	}
}

// ServiceTemplates is the built-in catalog (operator stages the images once
// via builds; the catalog never claims an image exists).
// FALLBACK ONLY: service_templates rows (migration 0034) + engine_defaults
// rows (migration 0035) are the source of truth, consulted first via
// ResolveTemplate. These built-ins apply only when the DB is unreachable,
// has no rows, or lacks the requested engine — never silently preferred.
func ServiceTemplates() []ServiceTemplate {
	return []ServiceTemplate{
		{Name: "postgres", Image: "custom://postgres-16", Version: "16", Ports: []int{5432}, VolumeMiB: 5120,
			Env:         map[string]string{"POSTGRES_DB": "app", "POSTGRES_USER": "${SERVICE_USER_POSTGRES}"},
			Secrets:     []string{"POSTGRES_PASSWORD"},
			Healthcheck: &ServiceHealth{Type: "tcp", Port: 5432, IntervalSec: 10},
			Description: "PostgreSQL 16 with a data volume and daily-backup default"},
		{Name: "redis", Image: "custom://redis-7", Version: "7", Ports: []int{6379}, VolumeMiB: 1024,
			Env:         map[string]string{},
			Secrets:     []string{"REDIS_PASSWORD"},
			Healthcheck: &ServiceHealth{Type: "tcp", Port: 6379, IntervalSec: 10},
			Description: "Redis 7 (passworded) with a data volume"},
		{Name: "mysql", Image: "custom://mysql-8", Version: "8", Ports: []int{3306}, VolumeMiB: 5120,
			Env:         map[string]string{"MYSQL_DATABASE": "app", "MYSQL_USER": "${SERVICE_USER_MYSQL}", "MYSQL_RANDOM_ROOT_PASSWORD": "yes"},
			Secrets:     []string{"MYSQL_PASSWORD"},
			Healthcheck: &ServiceHealth{Type: "tcp", Port: 3306, IntervalSec: 10},
			Description: "MySQL 8 with a data volume"},
		{Name: "mariadb", Image: "custom://mariadb-11", Version: "11", Ports: []int{3306}, VolumeMiB: 5120,
			Env:         map[string]string{"MARIADB_DATABASE": "app", "MARIADB_USER": "${SERVICE_USER_MARIADB}", "MARIADB_RANDOM_ROOT_PASSWORD": "yes"},
			Secrets:     []string{"MARIADB_PASSWORD"},
			Healthcheck: &ServiceHealth{Type: "tcp", Port: 3306, IntervalSec: 10},
			Description: "MariaDB 11 with a data volume"},
		{Name: "mongo", Image: "custom://mongo-7", Version: "7", Ports: []int{27017}, VolumeMiB: 5120,
			Env:         map[string]string{"MONGO_INITDB_ROOT_USERNAME": "${SERVICE_USER_MONGO}"},
			Secrets:     []string{"MONGO_INITDB_ROOT_PASSWORD"},
			Healthcheck: &ServiceHealth{Type: "tcp", Port: 27017, IntervalSec: 10},
			Description: "MongoDB 7 with a data volume"},
		{Name: "minio", Image: "custom://minio", Version: "latest", Ports: []int{9000, 9001}, VolumeMiB: 10240,
			Env:         map[string]string{"MINIO_ROOT_USER": "${SERVICE_USER_MINIO}"},
			Secrets:     []string{"MINIO_ROOT_PASSWORD"},
			Healthcheck: &ServiceHealth{Type: "http", Path: "/minio/health/live", Port: 9000, IntervalSec: 10},
			Description: "MinIO object storage with a data volume"},
	}
}

// ServiceTemplateStore is the minimal DB surface ResolveTemplate needs:
// full template rows from service_templates (migration 0034). *store.Store
// implements it. Import direction is safe: internal/store does not import
// internal/marketplace (grep-verified 2026-09-28), so no cycle.
type ServiceTemplateStore interface {
	ListServiceTemplates() []store.ServiceTemplateRow
}

// EngineDefaultGetter is optionally implemented alongside
// ServiceTemplateStore (it is, by *store.Store) to supply per-engine
// superuser/healthcheck rows from engine_defaults (migration 0035) for
// custom templates. Absence just means "derive the probe from built-ins".
type EngineDefaultGetter interface {
	GetEngineDefault(engine string) (store.EngineDefault, error)
}

// ResolveTemplate checks DB rows first, built-ins second. A DB miss covers
// all three fallback cases identically: lister nil, DB unreachable (store
// logs and returns nil), or rows present but engine unknown — each falls
// through to FindTemplate, so provisioning degrades to built-ins instead
// of failing. Custom rows map to ServiceTemplate with the healthcheck from
// the engine_defaults hc_* cols (same engine name); when that row is
// missing the built-in probe for the same engine is reused, else nil.
func ResolveTemplate(lister ServiceTemplateStore, name string) (ServiceTemplate, error) {
	if lister != nil {
		for _, row := range lister.ListServiceTemplates() {
			if strings.EqualFold(row.Name, name) {
				return customTemplate(lister, row), nil
			}
		}
	}
	return FindTemplate(name)
}

// customTemplate maps one DB row to a ServiceTemplate (the " (custom)"
// suffix marks operator-managed rows in listings and provision notes).
func customTemplate(lister ServiceTemplateStore, row store.ServiceTemplateRow) ServiceTemplate {
	t := ServiceTemplate{
		Name: row.Name, Image: row.Image, Version: row.Version,
		Ports: row.Ports, VolumeMiB: row.VolumeMiB, Env: row.Env,
		Secrets: row.Secrets, Description: row.Description + " (custom)",
	}
	// Probe: engine_defaults hc_* cols first (DB truth).
	if g, ok := lister.(EngineDefaultGetter); ok {
		if d, err := g.GetEngineDefault(row.Name); err == nil && d.HCPort > 0 {
			hc := &ServiceHealth{Type: d.HCType, Port: d.HCPort, IntervalSec: d.HCInterval}
			if hc.Type == "" {
				hc.Type = "tcp"
			}
			if hc.IntervalSec <= 0 {
				hc.IntervalSec = 10
			}
			// engine_defaults carries no path col; minio's live endpoint is
			// the only http probe in the catalog, so it is restored here.
			if hc.Type == "http" && hc.Path == "" && strings.EqualFold(row.Name, "minio") {
				hc.Path = "/minio/health/live"
			}
			t.Healthcheck = hc
			return t
		}
	}
	// Probe fallback: the built-in template's probe for the same engine.
	if b, err := FindTemplate(row.Name); err == nil {
		t.Healthcheck = b.Healthcheck
	}
	return t
}

// FindTemplate returns a template by name (case-insensitive).
func FindTemplate(name string) (ServiceTemplate, error) {
	for _, t := range ServiceTemplates() {
		if strings.EqualFold(t.Name, name) {
			return t, nil
		}
	}
	return ServiceTemplate{}, fmt.Errorf("marketplace: unknown service template %q", name)
}
