package protocol

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func testLeaseToken() string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
}

func testCommandV2(now time.Time) CommandV2 {
	return CommandV2{
		ProtocolVersion: VersionV2,
		ID:              "cmd_1", NodeID: "node_1", OperationID: "op_1", Attempt: 2,
		Kind: "vm.apply", LeaseToken: testLeaseToken(), LeaseExpiresAt: now.Add(time.Minute),
		Payload: json.RawMessage(`{"vm_id":"vm_1"}`), State: "leased", CreatedAt: now,
	}
}

func testResultV2(command CommandV2) CommandResultV2 {
	return CommandResultV2{
		ProtocolVersion: VersionV2, CommandID: command.ID, OperationID: command.OperationID,
		NodeID: command.NodeID, Attempt: command.Attempt, Kind: command.Kind,
		LeaseToken: command.LeaseToken, Success: true, State: "running",
	}
}

func TestV1CommandAndResultJSONRemainCompatible(t *testing.T) {
	legacyCommand := `{"id":"cmd-old","node_id":"node-old","operation_id":"op-old","attempt":1,"kind":"vm.stop","payload":{},"state":"sent","created_at":"2026-11-10T00:00:00Z"}`
	var command Command
	if err := json.Unmarshal([]byte(legacyCommand), &command); err != nil {
		t.Fatalf("decode legacy v1 command: %v", err)
	}
	if command.ID != "cmd-old" || command.OperationID != "op-old" || command.Attempt != 1 || command.Kind != "vm.stop" {
		t.Fatalf("legacy command changed: %+v", command)
	}
	legacyResult := `{"command_id":"cmd-old","operation_id":"op-old","node_id":"node-old","attempt":1,"success":true,"state":"stopped"}`
	var result CommandResult
	if err := json.Unmarshal([]byte(legacyResult), &result); err != nil {
		t.Fatalf("decode legacy v1 result: %v", err)
	}
	if result.CommandID != "cmd-old" || result.Attempt != 1 || !result.Success || result.State != "stopped" {
		t.Fatalf("legacy result changed: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("protocol_version")) || bytes.Contains(encoded, []byte("lease_token")) {
		t.Fatalf("v1 result unexpectedly emits v2 fields: %s", encoded)
	}
}

func TestV2CommandRequiresValidVersionLeaseAttemptKindAndExpiry(t *testing.T) {
	now := time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC)
	valid := testCommandV2(now)
	if err := valid.ValidateAt(now); err != nil {
		t.Fatalf("valid v2 command rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*CommandV2)
		want   error
	}{
		{"missing lease token", func(command *CommandV2) { command.LeaseToken = "" }, ErrInvalidLeaseToken},
		{"malformed lease token", func(command *CommandV2) { command.LeaseToken = strings.Repeat("!", 43) }, ErrInvalidLeaseToken},
		{"wrong protocol version", func(command *CommandV2) { command.ProtocolVersion = Version }, ErrInvalidLeasedMessage},
		{"unknown command kind", func(command *CommandV2) { command.Kind = "shell.exec" }, ErrInvalidLeasedMessage},
		{"nonpositive attempt", func(command *CommandV2) { command.Attempt = 0 }, ErrInvalidLeasedMessage},
		{"expired lease", func(command *CommandV2) { command.LeaseExpiresAt = now }, ErrLeaseExpired},
		{"missing payload", func(command *CommandV2) { command.Payload = nil }, ErrInvalidLeasedMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := valid
			tc.mutate(&command)
			if err := command.ValidateAt(now); !errors.Is(err, tc.want) {
				t.Fatalf("validation error=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestV2ResultRequiresLeaseAndMatchesClaimFences(t *testing.T) {
	now := time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC)
	command := testCommandV2(now)
	valid := testResultV2(command)
	if err := valid.ValidateForAt(command, now); err != nil {
		t.Fatalf("valid v2 result rejected: %v", err)
	}
	if subtle.ConstantTimeCompare([]byte(valid.LeaseToken), []byte(command.LeaseToken)) != 1 {
		t.Fatal("test lease tokens differ")
	}

	missingToken := valid
	missingToken.LeaseToken = ""
	if err := missingToken.Validate(); !errors.Is(err, ErrInvalidLeaseToken) {
		t.Fatalf("missing result lease token error=%v", err)
	}
	invalidToken := valid
	invalidToken.LeaseToken = strings.Repeat("!", 43)
	if err := invalidToken.Validate(); !errors.Is(err, ErrInvalidLeaseToken) {
		t.Fatalf("invalid result lease token error=%v", err)
	}

	stale := valid
	stale.Attempt--
	if err := stale.ValidateForAt(command, now); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("stale attempt error=%v", err)
	}
	wrongKind := valid
	wrongKind.Kind = "vm.stop"
	wrongKind.State = "stopped"
	if err := wrongKind.ValidateForAt(command, now); !errors.Is(err, ErrCommandKind) {
		t.Fatalf("wrong command kind error=%v", err)
	}
	wrongLease := valid
	wrongLease.LeaseToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	if err := wrongLease.ValidateForAt(command, now); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("wrong lease error=%v", err)
	}
	wrongNode := valid
	wrongNode.NodeID = "node_other"
	if err := wrongNode.ValidateForAt(command, now); !errors.Is(err, ErrCommandIdentity) {
		t.Fatalf("wrong node error=%v", err)
	}
	wrongOperation := valid
	wrongOperation.OperationID = "op_other"
	if err := wrongOperation.ValidateForAt(command, now); !errors.Is(err, ErrCommandIdentity) {
		t.Fatalf("wrong operation error=%v", err)
	}
	failedResult := valid
	failedResult.Success = false
	failedResult.Error = "runtime error"
	failedResult.State = "failed"
	if err := failedResult.ValidateForAt(command, now); err != nil {
		t.Fatalf("valid failed result rejected: %v", err)
	}
}
