package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"porter/agent/internal/client"
	"porter/agent/internal/localstate"
	"porter/agent/internal/store"
	"porter/protocol"
)

const (
	DefaultStateDir = "/var/lib/porter-agent"
	DefaultKernel   = "/var/lib/porter-agent/vmlinux"
	DefaultRootfs   = "/var/lib/porter-agent/rootfs.ext4"
)

type Options struct {
	Server     string
	Hostname   string
	StateDir   string
	Kernel     string
	Rootfs     string
	TokenStdin bool
	TLS        client.TLSOptions
}

type crashPoint string

const (
	crashAfterPendingConfig   crashPoint = "after_pending_config"
	crashAfterRecoveryConfig  crashPoint = "after_recovery_config"
	crashAfterRejectedOutcome crashPoint = "after_rejected_outcome"
)

type Prepared struct {
	options        Options
	dataDir        string
	configPath     string
	credentialPath string
	serviceConfig  localstate.ServiceConfig
	store          *store.Store
	joinLock       *os.File
	submitted      bool
	crashInjector  func(crashPoint) error
}

// Prepare validates non-secret inputs and creates the protected service config
// before the caller prompts for or reads any one-time enrollment credential.
func Prepare(options Options) (*Prepared, error) {
	return prepareWithCrashInjector(options, nil)
}

func prepareWithCrashInjector(options Options, inject func(crashPoint) error) (*Prepared, error) {
	options.Server = strings.TrimRight(strings.TrimSpace(options.Server), "/")
	if err := client.ValidateEnrollmentURL(options.Server); err != nil {
		return nil, err
	}
	if options.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil || strings.TrimSpace(hostname) == "" {
			return nil, fmt.Errorf("could not determine agent hostname")
		}
		options.Hostname = hostname
	}
	request := protocol.AgentEnrollmentRequestV2{ProtocolVersion: protocol.VersionV2, Hostname: options.Hostname}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if _, err := client.NewHTTPClient(options.TLS); err != nil {
		return nil, fmt.Errorf("configure control-plane TLS before token input: %w", err)
	}
	if options.StateDir == "" {
		options.StateDir = DefaultStateDir
	}
	dataDir, err := localstate.EnsurePrivateDir(options.StateDir)
	if err != nil {
		return nil, err
	}
	joinLock, err := localstate.AcquireJoinLock(dataDir)
	if err != nil {
		return nil, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = joinLock.Close()
		}
	}()
	if options.Kernel == "" {
		options.Kernel = filepath.Join(dataDir, "vmlinux")
	}
	if options.Rootfs == "" {
		options.Rootfs = filepath.Join(dataDir, "rootfs.ext4")
	}
	configPath := filepath.Join(dataDir, "agent-service.json")
	st, err := store.Open(filepath.Join(dataDir, "porter-agent.db"))
	if err != nil {
		return nil, fmt.Errorf("open agent state: %w", err)
	}
	cfg, err := st.LoadConfig()
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("read existing agent identity: %w", err)
	}
	priorAttempt, hasAttempt, err := st.JoinAttemptRecovery()
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("check prior enrollment attempt: %w", err)
	}
	priorOutcome := priorAttempt.Outcome
	if _, statErr := os.Lstat(configPath); statErr == nil {
		previous, readErr := localstate.ReadServiceConfigForJoin(configPath)
		if readErr != nil {
			_ = st.Close()
			return nil, fmt.Errorf("inspect existing agent service configuration: %w", readErr)
		}
		canClearPreSendConfig := cfg.NodeID == "" && (!hasAttempt || priorOutcome == "rejected")
		if canClearPreSendConfig && (previous.State == "pending" || previous.State == "recovery_required") {
			if err := os.Remove(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				_ = st.Close()
				return nil, fmt.Errorf("remove stale pre-send enrollment configuration: %w", err)
			}
		} else if previous.State == "recovery_required" {
			_ = st.Close()
			hostnameHint := "the exact hostname printed by that join attempt"
			outcomeHint := ""
			if priorAttempt.Hostname != "" {
				hostnameHint = fmt.Sprintf("the exact hostname printed by that join attempt (%q)", priorAttempt.Hostname)
			}
			if hasAttempt {
				outcomeHint = fmt.Sprintf(" with persisted outcome %q", priorOutcome)
			}
			return nil, fmt.Errorf("a previous join may have reached the handler at %s%s; do not replay its token. Use %s and this time to find the Fleet row, verify the exact node ID, then rotate credentials through admin Fleet; stop if the match is missing or ambiguous", previous.EnrollmentStartedAt, outcomeHint, hostnameHint)
		} else {
			_ = st.Close()
			return nil, fmt.Errorf("agent service configuration already exists; refusing a second enrollment attempt")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = st.Close()
		return nil, fmt.Errorf("inspect existing agent service configuration: %w", statErr)
	}
	credentialPath := localstate.CredentialPath(dataDir)
	if _, err := os.Lstat(credentialPath); err == nil {
		_ = st.Close()
		return nil, fmt.Errorf("agent node credential already exists; refusing to overwrite local identity")
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = st.Close()
		return nil, fmt.Errorf("inspect existing agent credential: %w", err)
	}
	if cfg.NodeID != "" {
		_ = st.Close()
		return nil, fmt.Errorf("agent already has node identity %q; refusing duplicate enrollment", cfg.NodeID)
	}
	if hasAttempt && priorOutcome != "rejected" {
		_ = st.Close()
		return nil, fmt.Errorf("%s", priorJoinReplayBlock(priorAttempt))
	}
	serviceConfig := localstate.ServiceConfig{
		State:          "pending",
		Server:         options.Server,
		CredentialPath: credentialPath,
		StateDir:       dataDir,
		Kernel:         options.Kernel,
		Rootfs:         options.Rootfs,
		ServerCAFile:   options.TLS.ServerCAFile,
		ClientCertFile: options.TLS.ClientCertFile,
		ClientKeyFile:  options.TLS.ClientKeyFile,
	}
	if err := localstate.CreatePendingServiceConfig(configPath, serviceConfig); err != nil {
		_ = st.Close()
		return nil, err
	}
	if inject != nil {
		if err := inject(crashAfterPendingConfig); err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	keepLock = true
	return &Prepared{
		options:        options,
		dataDir:        dataDir,
		configPath:     configPath,
		credentialPath: credentialPath,
		serviceConfig:  serviceConfig,
		store:          st,
		joinLock:       joinLock,
		crashInjector:  inject,
	}, nil
}

func (p *Prepared) Close() error {
	if p == nil {
		return nil
	}
	var closeErr error
	if !p.submitted {
		closeErr = p.CancelBeforeSubmission()
	}
	if p.store != nil {
		if err := p.store.Close(); closeErr == nil {
			closeErr = err
		}
		p.store = nil
	}
	if p.joinLock != nil {
		if err := p.joinLock.Close(); closeErr == nil {
			closeErr = err
		}
		p.joinLock = nil
	}
	return closeErr
}

// Enroll sends at most one request for this invocation. Every post-send failure
// remains fenced; only a local pre-send rejection can be corrected and retried.
func (p *Prepared) Enroll(ctx context.Context, joinToken string) (protocol.AgentEnrollmentResponseV2, error) {
	var empty protocol.AgentEnrollmentResponseV2
	if p == nil || p.store == nil {
		return empty, fmt.Errorf("agent join is not prepared")
	}
	if p.submitted {
		return empty, fmt.Errorf("this join invocation has already submitted its one enrollment request; do not retry a sent token")
	}
	if err := client.ValidateJoinToken(joinToken); err != nil {
		return empty, client.NewEnrollmentOutcomeRejectedError(err)
	}
	started := time.Now().UTC()
	p.serviceConfig.State = "recovery_required"
	p.serviceConfig.EnrollmentStartedAt = started.Format(time.RFC3339Nano)
	if err := localstate.WriteServiceConfig(p.configPath, p.serviceConfig); err != nil {
		return empty, fmt.Errorf("persist pending enrollment state before network request: %w", err)
	}
	if err := p.injectCrash(crashAfterRecoveryConfig); err != nil {
		return empty, err
	}
	if err := p.store.SaveJoinAttempt(store.JoinAttempt{
		Server:    p.options.Server,
		Hostname:  p.options.Hostname,
		StartedAt: started,
		Outcome:   "submitted",
	}); err != nil {
		return empty, fmt.Errorf("persist enrollment recovery metadata before network request: %w", err)
	}
	p.submitted = true
	request := protocol.AgentEnrollmentRequestV2{ProtocolVersion: protocol.VersionV2, Hostname: p.options.Hostname}
	response, err := client.EnrollV2(ctx, p.options.Server, joinToken, request, p.options.TLS)
	if err != nil {
		var rejected *client.EnrollmentOutcomeRejectedError
		if errors.As(err, &rejected) {
			if updateErr := p.store.UpdateJoinAttemptOutcome("rejected"); updateErr != nil {
				return empty, fmt.Errorf("enrollment was rejected before transmission, but the rejected outcome could not be persisted: %w", updateErr)
			}
			if err := p.injectCrash(crashAfterRejectedOutcome); err != nil {
				return empty, err
			}
			if removeErr := os.Remove(p.configPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return empty, fmt.Errorf("enrollment was rejected before transmission, but local pending state could not be cleared: %w", removeErr)
			}
			p.submitted = false
			return empty, err
		}
		var unknown *client.EnrollmentOutcomeUnknownError
		if !errors.As(err, &unknown) {
			unknown = client.NewEnrollmentOutcomeUnknownError(err)
		}
		return empty, p.markOutcomeUnknown(started, unknown)
	}
	if err := localstate.WriteCredential(p.credentialPath, response.NodeToken); err != nil {
		_ = p.store.UpdateJoinAttemptOutcome("credential_persist_failed")
		return empty, fmt.Errorf("server enrolled node %s for hostname %q at %s, but its credential could not be persisted; do not retry. Use admin Fleet to rotate credentials for this exact node ID; stop if the node identity is uncertain", response.NodeID, p.options.Hostname, started.Format(time.RFC3339))
	}
	if err := p.store.SaveConfig(store.Config{
		Server:         response.ControlPlaneURL,
		NodeID:         response.NodeID,
		CredentialPath: p.credentialPath,
	}); err != nil {
		_ = p.store.UpdateJoinAttemptOutcome("metadata_persist_failed")
		return empty, fmt.Errorf("node %s was enrolled for hostname %q at %s and its credential file was saved, but local identity metadata could not be persisted; do not retry. Repair local identity or rotate credentials for this exact node ID through admin Fleet", response.NodeID, p.options.Hostname, started.Format(time.RFC3339))
	}
	if err := p.store.UpdateJoinAttemptOutcome("registered"); err != nil {
		return empty, fmt.Errorf("node %s was enrolled and local credentials were saved, but recovery metadata could not be finalized: %w", response.NodeID, err)
	}
	p.serviceConfig.State = "ready"
	p.serviceConfig.NodeID = response.NodeID
	p.serviceConfig.CredentialGeneration = response.CredentialGeneration
	p.serviceConfig.Server = response.ControlPlaneURL
	if err := localstate.WriteServiceConfig(p.configPath, p.serviceConfig); err != nil {
		return empty, fmt.Errorf("node %s was enrolled and credential metadata was saved, but the service config could not be finalized; the agent will not start until it is repaired", response.NodeID)
	}
	return response, nil
}

func priorJoinReplayBlock(attempt store.JoinAttempt) string {
	details := "Use the exact hostname and enrollment time printed by the original join attempt"
	if attempt.Hostname != "" && !attempt.StartedAt.IsZero() {
		details = fmt.Sprintf("Use the exact hostname %q and enrollment time %s stored for the original join attempt", attempt.Hostname, attempt.StartedAt.UTC().Format(time.RFC3339Nano))
	}
	return fmt.Sprintf("a prior enrollment attempt has outcome %q and may have reached the handler; do not replay its token. %s to find the Fleet row, verify the exact node ID, then rotate credentials through admin Fleet; stop if the match is missing or ambiguous", attempt.Outcome, details)
}

func (p *Prepared) injectCrash(point crashPoint) error {
	if p.crashInjector == nil {
		return nil
	}
	return p.crashInjector(point)
}

func (p *Prepared) markOutcomeUnknown(started time.Time, cause error) error {
	unknown := client.NewEnrollmentOutcomeUnknownError(cause)
	if err := p.store.UpdateJoinAttemptOutcome("outcome_unknown"); err != nil {
		return fmt.Errorf("join attempt for hostname %q started at %s: %w; failed to persist outcome_unknown (%v); recovery-required service config remains to block replay", p.options.Hostname, started.Format(time.RFC3339), unknown, err)
	}
	return fmt.Errorf("join attempt for hostname %q started at %s: %w", p.options.Hostname, started.Format(time.RFC3339), unknown)
}

func (p *Prepared) StateDir() string          { return p.dataDir }
func (p *Prepared) CredentialPath() string    { return p.credentialPath }
func (p *Prepared) ServiceConfigPath() string { return p.configPath }
