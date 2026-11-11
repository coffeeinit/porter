package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultOperatorURL = "https://127.0.0.1:8080"

func runCLI(args []string) int {
	if len(args) == 0 {
		return usage("missing command")
	}

	cmd := args[0]
	switch cmd {
	case "status", "nodes":
		if len(args) != 1 {
			return usage("porter " + cmd + " takes no arguments")
		}
	case "join-token":
		if len(args) != 3 {
			return usage("porter join-token <same_host|remote_host> <https-origin>")
		}
		if args[1] != "same_host" && args[1] != "remote_host" {
			return usage("join-token target must be same_host or remote_host")
		}
		if !isHTTPSOrigin(args[2]) {
			return usage("join-token control-plane URL must be an HTTPS origin without a path, query, fragment, or user information")
		}
	default:
		if isRetiredCLICommand(cmd) {
			fmt.Fprintf(os.Stderr, "porter %s is not supported by the PostgreSQL Fleet operator CLI; no request was sent\n", cmd)
			return 2
		}
		return usage("unknown command")
	}

	base := strings.TrimRight(os.Getenv("PORTER_URL"), "/")
	if base == "" {
		base = defaultOperatorURL
	}
	if !isHTTPSOrigin(base) {
		fmt.Fprintln(os.Stderr, "PORTER_URL must be an HTTPS origin without a path, query, fragment, or user information")
		return 2
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
			b, err := json.Marshal(in)
			if err != nil {
				return nil, fmt.Errorf("encode request: %w", err)
			}
			body = bytes.NewReader(b)
		}
		request, err := http.NewRequest(method, base+path, body)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")

		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()

		raw, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, operatorHTTPError(response.Status, raw)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, nil
		}
		var out interface{}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode response JSON: %w", err)
		}
		return out, nil
	}

	var out interface{}
	var err error
	switch cmd {
	case "status":
		out, err = do(http.MethodGet, "/api/v1/fleet/capabilities", nil)
	case "nodes":
		out, err = do(http.MethodGet, "/api/v1/fleet/nodes", nil)
	case "join-token":
		out, err = do(http.MethodPost, "/api/v1/fleet/join-tokens", struct {
			Target          string `json:"target"`
			ControlPlaneURL string `json:"control_plane_url"`
		}{Target: args[1], ControlPlaneURL: args[2]})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	formatted, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: encode response:", err)
		return 1
	}
	fmt.Println(string(formatted))
	return 0
}

func isHTTPSOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil &&
		parsed.Scheme == "https" &&
		parsed.Host != "" &&
		parsed.User == nil &&
		parsed.Path == "" &&
		parsed.RawQuery == "" &&
		parsed.Fragment == "" &&
		parsed.Opaque == ""
}

func operatorHTTPError(status string, body []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		if envelope.Error.Code != "" {
			return fmt.Errorf("%s: %s (%s)", status, envelope.Error.Message, envelope.Error.Code)
		}
		return fmt.Errorf("%s: %s", status, envelope.Error.Message)
	}
	return fmt.Errorf("%s: %s", status, strings.TrimSpace(string(body)))
}

func isRetiredCLICommand(cmd string) bool {
	switch cmd {
	case "projects", "deploy", "logs", "scale", "rollback", "backup":
		return true
	default:
		return false
	}
}

func usage(msg string) int {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, "commands: status | nodes | join-token <same_host|remote_host> <https-origin>")
	fmt.Fprintln(os.Stderr, "retired (no request sent): projects | deploy | logs | scale | rollback | backup")
	return 2
}
