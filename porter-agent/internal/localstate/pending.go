package localstate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func ReadServiceConfigPending(path string) (ServiceConfig, error) {
	config, err := readJoinServiceConfig(path)
	if err != nil {
		return config, err
	}
	if config.State != "pending" {
		return config, fmt.Errorf("agent service configuration is not pending")
	}
	return config, nil
}

// ReadServiceConfigForJoin reads only the protected join-state file and accepts
// pending or recovery-required states; ready service config is not join state.
func ReadServiceConfigForJoin(path string) (ServiceConfig, error) {
	return readJoinServiceConfig(path)
}

func readJoinServiceConfig(path string) (ServiceConfig, error) {
	var config ServiceConfig
	data, err := readPrivate(path)
	if err != nil {
		return config, fmt.Errorf("read agent join service configuration: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode agent join service configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return config, fmt.Errorf("agent join service configuration contains trailing data")
	}
	if (config.State != "pending" && config.State != "recovery_required") || config.Server == "" || config.CredentialPath == "" || config.StateDir == "" {
		return config, fmt.Errorf("agent join service configuration is incomplete")
	}
	if config.State == "recovery_required" && config.EnrollmentStartedAt == "" {
		return config, fmt.Errorf("recovery-required join configuration is missing its start time")
	}
	return config, nil
}
