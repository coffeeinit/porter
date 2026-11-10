package localstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	DirectoryMode   = 0o700
	PrivateFileMode = 0o600
)

func validCredential(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

// ServiceConfig contains non-secret configuration consumed by the agent. The
// node bearer is deliberately kept in a separate file and never serialized here.
type ServiceConfig struct {
	State                string `json:"state"`
	Server               string `json:"server"`
	NodeID               string `json:"node_id,omitempty"`
	CredentialPath       string `json:"credential_path"`
	CredentialGeneration int64  `json:"credential_generation,omitempty"`
	StateDir             string `json:"state_dir"`
	Kernel               string `json:"kernel,omitempty"`
	Rootfs               string `json:"rootfs,omitempty"`
	ServerCAFile         string `json:"server_ca_file,omitempty"`
	ClientCertFile       string `json:"client_cert_file,omitempty"`
	ClientKeyFile        string `json:"client_key_file,omitempty"`
	EnrollmentStartedAt  string `json:"enrollment_started_at,omitempty"`
}

// EnsurePrivateDir creates a private data directory owned by the effective
// user. When run through sudo this produces the required root:root 0700 dir.
func EnsurePrivateDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("agent data directory is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve agent data directory: %w", err)
	}
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) {
		return "", fmt.Errorf("refusing to use the filesystem root as the agent data directory")
	}
	created := false
	if _, err := os.Lstat(abs); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", fmt.Errorf("create agent data directory parent: %w", err)
		}
		if err := os.Mkdir(abs, DirectoryMode); err == nil {
			created = true
		} else if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create agent data directory: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect agent data directory: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect agent data directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("agent data directory must be a real directory, not a symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("cannot verify agent data directory ownership")
	}
	uid, gid := uint32(os.Geteuid()), expectedGID()
	if stat.Uid != uid || stat.Gid != gid {
		return "", fmt.Errorf("agent data directory must be owned by the effective user and group")
	}
	if created && info.Mode().Perm() != DirectoryMode {
		if err := os.Chmod(abs, DirectoryMode); err != nil {
			return "", fmt.Errorf("secure newly created agent data directory: %w", err)
		}
	} else if info.Mode().Perm() != DirectoryMode {
		return "", fmt.Errorf("existing agent data directory must already have mode 0700; refusing to change it")
	}
	info, err = os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("verify agent data directory: %w", err)
	}
	stat, ok = info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != DirectoryMode || stat.Uid != uid || stat.Gid != gid {
		return "", fmt.Errorf("agent data directory is not owned and protected as required")
	}
	return abs, nil
}

// AcquireJoinLock serializes enrollment attempts that share one local identity
// directory. O_NOFOLLOW plus owner/mode checks fail closed on unsafe lock paths.
func AcquireJoinLock(dataDir string) (*os.File, error) {
	path := filepath.Join(dataDir, ".porter-agent-join.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, PrivateFileMode)
	if err != nil {
		return nil, fmt.Errorf("open agent join lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("agent join lock must be a regular file")
	}
	if err := verifyOwnerAndMode(path, info, PrivateFileMode); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("agent join lock is unsafe: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("agent join lock must have exactly one link")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another enrollment is already using this agent data directory")
	}
	return file, nil
}

// CredentialPath returns the one fixed location for the local node bearer.
func CredentialPath(dataDir string) string {
	return filepath.Join(dataDir, "node-credential")
}

// WriteCredential atomically replaces the credential file with mode 0600. The
// file is never written to SQLite, a temporary file outside the data directory,
// or a log.
func WriteCredential(path, credential string) error {
	if !validCredential(credential) {
		return fmt.Errorf("node credential is empty or contains whitespace")
	}
	if err := writePrivateAtomic(path, []byte(credential+"\n")); err != nil {
		return fmt.Errorf("persist node credential: %w", err)
	}
	return nil
}

// ReadCredential refuses symlinks, non-regular files, permissive modes, and
// files owned by a different user.
func ReadCredential(path string) (string, error) {
	data, err := readPrivate(path)
	if err != nil {
		return "", fmt.Errorf("read node credential: %w", err)
	}
	credential := strings.TrimSuffix(string(data), "\n")
	if !validCredential(credential) {
		return "", fmt.Errorf("node credential file is invalid")
	}
	return credential, nil
}

func WriteServiceConfig(path string, config ServiceConfig) error {
	if config.State != "pending" && config.State != "ready" && config.State != "recovery_required" {
		return fmt.Errorf("invalid agent service configuration state")
	}
	if config.State == "ready" && (config.NodeID == "" || config.CredentialPath == "" || config.CredentialGeneration < 1) {
		return fmt.Errorf("ready agent service configuration is incomplete")
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent service configuration: %w", err)
	}
	if err := writePrivateAtomic(path, append(data, '\n')); err != nil {
		return fmt.Errorf("persist agent service configuration: %w", err)
	}
	return nil
}

// CreatePendingServiceConfig creates the first enrollment config without
// replacing any pre-existing service identity.
func CreatePendingServiceConfig(path string, config ServiceConfig) error {
	if config.State != "pending" || config.Server == "" || config.CredentialPath == "" || config.StateDir == "" {
		return fmt.Errorf("pending agent service configuration is incomplete")
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent service configuration: %w", err)
	}
	if err := writePrivateAtomicMode(path, append(data, '\n'), true); err != nil {
		return fmt.Errorf("create pending agent service configuration: %w", err)
	}
	return nil
}

func ReadServiceConfig(path string) (ServiceConfig, error) {
	var config ServiceConfig
	data, err := readPrivate(path)
	if err != nil {
		return config, fmt.Errorf("read agent service configuration: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode agent service configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return config, fmt.Errorf("agent service configuration contains trailing data")
	}
	if config.State != "ready" || config.NodeID == "" || config.Server == "" || config.CredentialPath == "" || config.StateDir == "" {
		return config, fmt.Errorf("agent service configuration is not ready")
	}
	return config, nil
}

func writePrivateAtomic(path string, data []byte) error {
	return writePrivateAtomicMode(path, data, false)
}

func writePrivateAtomicMode(path string, data []byte, exclusive bool) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect private-file directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("private-file parent must be a real directory")
	}
	if err := verifyOwnerAndMode(parent, info, DirectoryMode); err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".porter-agent-private-*")
	if err != nil {
		return fmt.Errorf("create private temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(PrivateFileMode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set private-file mode: %w", err)
	}
	if os.Geteuid() == 0 && os.Getegid() != 0 {
		if err := temp.Chown(0, 0); err != nil {
			_ = temp.Close()
			return fmt.Errorf("set root-owned private-file owner: %w", err)
		}
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write private temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync private temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close private temporary file: %w", err)
	}
	if exclusive {
		if err := os.Link(tempName, path); err != nil {
			return fmt.Errorf("atomically install private file without replacing existing state: %w", err)
		}
		if err := os.Remove(tempName); err != nil {
			return fmt.Errorf("remove private temporary file: %w", err)
		}
	} else if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("atomically install private file: %w", err)
	}
	dir, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open private-file directory for sync: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return fmt.Errorf("sync private-file directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close private-file directory: %w", closeErr)
	}
	return nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("private file must be a regular file, not a symlink")
	}
	if err := verifyOwnerAndMode(path, info, PrivateFileMode); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func verifyOwnerAndMode(path string, info os.FileInfo, mode os.FileMode) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot verify private-file ownership: %s", path)
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Gid != expectedGID() {
		return fmt.Errorf("private path has unexpected owner: %s", path)
	}
	if info.Mode().Perm() != mode {
		return fmt.Errorf("private path must have mode %04o: %s", mode, path)
	}
	return nil
}

func expectedGID() uint32 {
	if os.Geteuid() == 0 {
		return 0
	}
	return uint32(os.Getegid())
}
