package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func runCLI(args []string) int {
	if len(args) == 0 {
		return 2
	}
	base := strings.TrimRight(os.Getenv("PORTER_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	token := os.Getenv("PORTER_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "PORTER_TOKEN is required")
		return 2
	}
	client := &http.Client{Timeout: 30 * time.Second}
	do := func(method, path string, in interface{}) (interface{}, error) {
		var body io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, base+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out interface{}
		_ = json.Unmarshal(raw, &out)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if m, ok := out.(map[string]interface{}); ok {
				if e, ok := m["error"].(string); ok {
					return nil, fmt.Errorf("%s", e)
				}
			}
			return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
		}
		return out, nil
	}
	cmd := args[0]
	var out interface{}
	var err error
	switch cmd {
	case "status":
		out, err = do("GET", "/healthz", nil)
	case "nodes":
		out, err = do("GET", "/v1/nodes", nil)
	case "projects":
		out, err = do("GET", "/v1/projects", nil)
	case "join-token":
		out, err = do("POST", "/v1/fleet/join-tokens", map[string]interface{}{"ttl_seconds": 900})
	case "backup":
		out, err = do("POST", "/v1/system/backup", nil)
	case "logs":
		if len(args) < 2 {
			return usage("porter logs <vm_id>")
		}
		out, err = do("GET", "/v1/logs?vm_id="+args[1], nil)
	case "scale":
		if len(args) < 3 {
			return usage("porter scale <app_id> <replicas>")
		}
		n, _ := strconv.Atoi(args[2])
		out, err = do("POST", "/v1/apps/"+args[1]+"/scale", map[string]int{"replicas": n})
	case "rollback":
		if len(args) < 2 {
			return usage("porter rollback <app_id>")
		}
		out, err = do("POST", "/v1/apps/"+args[1]+"/rollback", nil)
	case "deploy":
		if len(args) < 4 {
			return usage("porter deploy <project_id> <name> <image> [replicas]")
		}
		replicas := 1
		if len(args) > 4 {
			replicas, _ = strconv.Atoi(args[4])
		}
		app, e := do("POST", "/v1/projects/"+args[1]+"/apps", map[string]interface{}{"name": args[2], "image": args[3], "replicas": replicas, "port": 8080, "vcpus": 1, "memory_mb": 256})
		if e != nil {
			err = e
			break
		}
		m, _ := app.(map[string]interface{})
		id, _ := m["id"].(string)
		out, err = do("POST", "/v1/apps/"+id+"/deployments", map[string]interface{}{"image": args[3], "replicas": replicas})
	default:
		return usage("unknown command")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return 0
}

func usage(msg string) int {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, "commands: status | nodes | projects | deploy | logs | scale | rollback | join-token | backup")
	return 2
}
