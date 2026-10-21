package imagebuilder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Framework string

const (
	FrameworkDockerfile Framework = "dockerfile"
	FrameworkLaravel    Framework = "laravel"
	FrameworkNextJS     Framework = "nextjs"
	FrameworkReact      Framework = "react"
	FrameworkNode       Framework = "node"
	FrameworkStatic     Framework = "static"
	FrameworkUnknown    Framework = "unknown"
)

type Detection struct {
	Framework  Framework `json:"framework"`
	ContextDir string    `json:"context_dir"`
	Dockerfile string    `json:"dockerfile,omitempty"`
	Reason     string    `json:"reason"`
}

func DetectFramework(dir string) (Detection, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Detection{}, err
	}
	has := func(names ...string) bool {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(abs, n)); err == nil {
				return true
			}
		}
		return false
	}
	if has("Dockerfile", "dockerfile") {
		df := filepath.Join(abs, "Dockerfile")
		if _, err := os.Stat(df); err != nil {
			df = filepath.Join(abs, "dockerfile")
		}
		return Detection{Framework: FrameworkDockerfile, ContextDir: abs, Dockerfile: df, Reason: "Dockerfile found"}, nil
	}
	if has("composer.json", "artisan") {
		return Detection{Framework: FrameworkLaravel, ContextDir: abs, Reason: "composer.json/artisan detected"}, nil
	}
	if has("next.config.js", "next.config.mjs", "next.config.ts") {
		return Detection{Framework: FrameworkNextJS, ContextDir: abs, Reason: "next config detected"}, nil
	}
	if has("package.json") {
		b, _ := os.ReadFile(filepath.Join(abs, "package.json"))
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		_ = json.Unmarshal(b, &pkg)
		if _, ok := pkg.Dependencies["next"]; ok {
			return Detection{Framework: FrameworkNextJS, ContextDir: abs, Reason: "next dependency detected"}, nil
		}
		if _, ok := pkg.Dependencies["react"]; ok {
			return Detection{Framework: FrameworkReact, ContextDir: abs, Reason: "react dependency detected"}, nil
		}
		if _, ok := pkg.DevDependencies["vite"]; ok {
			return Detection{Framework: FrameworkReact, ContextDir: abs, Reason: "vite detected"}, nil
		}
		return Detection{Framework: FrameworkNode, ContextDir: abs, Reason: "package.json detected"}, nil
	}
	if has("index.html") {
		return Detection{Framework: FrameworkStatic, ContextDir: abs, Reason: "index.html detected"}, nil
	}
	return Detection{Framework: FrameworkUnknown, ContextDir: abs, Reason: "no known framework marker"}, nil
}

func EnsureDockerfile(d Detection) (string, error) {
	if d.Dockerfile != "" {
		return d.Dockerfile, nil
	}
	var body string
	switch d.Framework {
	case FrameworkLaravel:
		body = `FROM php:8.3-apache
RUN docker-php-ext-install pdo pdo_mysql
COPY . /var/www/html
RUN chown -R www-data:www-data /var/www/html/storage /var/www/html/bootstrap/cache 2>/dev/null || true
`
	case FrameworkNextJS:
		body = `FROM node:22-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build
EXPOSE 3000
CMD ["npm","start"]
`
	case FrameworkReact:
		body = `FROM node:22-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build
FROM nginx:alpine
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
`
	case FrameworkNode:
		body = `FROM node:22-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev || npm install --omit=dev
COPY . .
EXPOSE 3000
CMD ["npm","start"]
`
	case FrameworkStatic:
		body = `FROM nginx:alpine
COPY . /usr/share/nginx/html
EXPOSE 80
`
	default:
		return "", fmt.Errorf("cannot generate Dockerfile for framework %q", d.Framework)
	}
	path := filepath.Join(d.ContextDir, "Dockerfile.porter")
	return path, os.WriteFile(path, []byte(body), 0o644)
}

type composeFile struct {
	Services map[string]struct {
		Build   interface{} `yaml:"build"`
		Image   string      `yaml:"image"`
		Ports   []string    `yaml:"ports"`
		Command interface{} `yaml:"command"`
	} `yaml:"services"`
}

type ComposeService struct {
	Name       string   `json:"name"`
	Image      string   `json:"image,omitempty"`
	ContextDir string   `json:"context_dir,omitempty"`
	Dockerfile string   `json:"dockerfile,omitempty"`
	Ports      []string `json:"ports,omitempty"`
}

func ParseCompose(path string) (ComposeService, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ComposeService{}, err
	}
	var cf composeFile
	if err := yaml.Unmarshal(b, &cf); err != nil {
		return ComposeService{}, err
	}
	base := filepath.Dir(path)
	var chosen ComposeService
	for name, svc := range cf.Services {
		c := ComposeService{Name: name, Image: svc.Image, Ports: svc.Ports}
		switch v := svc.Build.(type) {
		case string:
			c.ContextDir = filepath.Join(base, v)
		case map[string]interface{}:
			if ctx, ok := v["context"].(string); ok {
				c.ContextDir = filepath.Join(base, ctx)
			}
			if df, ok := v["dockerfile"].(string); ok {
				c.Dockerfile = filepath.Join(c.ContextDir, df)
			}
		}
		if c.ContextDir == "" {
			c.ContextDir = base
		}
		if chosen.Name == "" || (chosen.Image != "" && c.ContextDir != "") {
			chosen = c
		}
	}
	if chosen.Name == "" {
		return ComposeService{}, fmt.Errorf("no compose services found")
	}
	if chosen.Dockerfile == "" {
		chosen.Dockerfile = filepath.Join(chosen.ContextDir, "Dockerfile")
	}
	return chosen, nil
}

func (b *Builder) BuildFromGit(ctx context.Context, repo, branch, subdir, name string, sizeMB int) (Result, error) {
	dir, err := os.MkdirTemp(b.WorkDir, "porter-git-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	clone := dir + "/src"
	args := []string{"clone", "--depth", "1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repo, clone)
	if out, err := execCommand(ctx, "git", args...); err != nil {
		return Result{}, fmt.Errorf("git clone: %w: %s", err, out)
	}
	ctxDir := clone
	if subdir != "" {
		ctxDir = filepath.Join(clone, subdir)
	}
	d, err := DetectFramework(ctxDir)
	if err != nil {
		return Result{}, err
	}
	if d.Framework == FrameworkUnknown {
		return Result{}, fmt.Errorf("unknown framework; add a Dockerfile")
	}
	df, err := EnsureDockerfile(d)
	if err != nil {
		return Result{}, err
	}
	return b.BuildDockerfile(ctx, d.ContextDir, df, name, sizeMB)
}

func (b *Builder) BuildCompose(ctx context.Context, composePath, name string, sizeMB int) (Result, error) {
	svc, err := ParseCompose(composePath)
	if err != nil {
		return Result{}, err
	}
	if svc.Image != "" && svc.ContextDir == "" {
		return b.BuildDockerImage(ctx, svc.Image, name, sizeMB)
	}
	df := svc.Dockerfile
	if _, err := os.Stat(df); err != nil {
		d, derr := DetectFramework(svc.ContextDir)
		if derr != nil {
			return Result{}, derr
		}
		df, derr = EnsureDockerfile(d)
		if derr != nil {
			return Result{}, derr
		}
	}
	return b.BuildDockerfile(ctx, svc.ContextDir, df, name, sizeMB)
}

func execCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
