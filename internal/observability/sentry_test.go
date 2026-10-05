package observability

import "testing"

func TestDisabledSentryIsNoop(t *testing.T) {
	cleanup, err := InitSentry(false, "", "test", "porter-test")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
}
