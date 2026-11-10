package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
)

func TestEnrollmentTokenFormatAndHTTPSOriginValidation(t *testing.T) {
	token, _, err := newEnrollmentToken()
	if err != nil || !validEnrollmentToken(token) {
		t.Fatalf("new enrollment token=%q err=%v", token, err)
	}
	for _, invalid := range []string{"", strings.TrimPrefix(token, enrollmentTokenPrefix), "ptenr_" + strings.Repeat("!", 43), token + "x"} {
		if validEnrollmentToken(invalid) {
			t.Errorf("invalid enrollment token %q accepted", invalid)
		}
	}
	canonical, err := validateEnrollmentControlPlaneURL("https://control.example/")
	if err != nil || canonical != "https://control.example" {
		t.Fatalf("canonical control-plane origin=%q err=%v", canonical, err)
	}
	for _, invalid := range []string{
		"", "http://control.example", "https://user@control.example", "https://control.example/path",
		"https://control.example/?token=x", "https://control.example/#fragment", "https://",
	} {
		if _, err := validateEnrollmentControlPlaneURL(invalid); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("invalid control-plane origin %q error=%v", invalid, err)
		}
	}
}

func TestEnrollmentTokenIssueRejectsBadTargetAndLifetimeBeforeDatabaseAccess(t *testing.T) {
	store := &Store{}
	now := time.Now().UTC()
	cases := []struct {
		name   string
		target v1.AgentEnrollmentTarget
		ttl    time.Duration
	}{
		{name: "invalid target", target: v1.AgentEnrollmentUnspecified, ttl: defaultAgentEnrollmentTTL},
		{name: "short lifetime", target: v1.AgentEnrollmentRemoteHost, ttl: time.Second},
		{name: "long lifetime", target: v1.AgentEnrollmentSameHost, ttl: maxAgentEnrollmentTTL + time.Second},
		{name: "zero time", target: v1.AgentEnrollmentSameHost, ttl: defaultAgentEnrollmentTTL},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			at := now
			if test.name == "zero time" {
				at = time.Time{}
			}
			_, err := store.CreateAgentEnrollmentTokenAt(context.Background(), test.target, "https://control.example", at, test.ttl)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("invalid enrollment issue error=%v", err)
			}
		})
	}
}
