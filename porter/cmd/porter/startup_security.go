package main

import (
	"fmt"
	"strings"
)

const (
	apiModeProduction  = "production"
	apiModeDevelopment = "development"
)

type startupSecurity struct {
	APIMode    string
	AdminToken string
}

// resolveStartupSecurity defaults to production and requires an explicit admin
// credential. It returns the configured value unchanged and never generates or
// logs credentials.
func resolveStartupSecurity(apiMode, adminToken string) (startupSecurity, error) {
	mode := strings.ToLower(strings.TrimSpace(apiMode))
	if mode == "" {
		mode = apiModeProduction
	}
	if mode != apiModeProduction && mode != apiModeDevelopment {
		return startupSecurity{}, fmt.Errorf("PORTER_API_MODE must be %q or %q", apiModeProduction, apiModeDevelopment)
	}
	if strings.TrimSpace(adminToken) == "" {
		return startupSecurity{}, fmt.Errorf("operator admin credential is required")
	}
	if strings.TrimSpace(adminToken) != adminToken || strings.ContainsAny(adminToken, " \t\r\n") {
		return startupSecurity{}, fmt.Errorf("operator admin credential must not contain whitespace")
	}
	return startupSecurity{APIMode: mode, AdminToken: adminToken}, nil
}
