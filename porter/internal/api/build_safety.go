package api

import (
	"os"
	"strings"
)

const unisolatedBuildOptIn = "PORTER_ENABLE_UNISOLATED_BUILDS"

// unisolatedBuildsEnabled is deliberately false by default: the current builder
// uses host Docker/Git and is not an isolation boundary for untrusted source.
func unisolatedBuildsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(unisolatedBuildOptIn)), "true")
}
