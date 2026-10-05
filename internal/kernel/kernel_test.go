package kernel

import (
	"strings"
	"testing"
)

func TestBuildSingleActive(t *testing.T) {
	m := NewManager()
	b, err := m.Start("6.18.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("6.18.1"); err == nil {
		t.Fatal("second active build must be rejected")
	}
	for _, next := range []string{BuildFetching, BuildConfiguring, BuildCompiling, BuildCompleted} {
		if err := b.Advance(next, 50); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Advance(BuildFetching, 0); err == nil {
		t.Fatal("terminal state must stick")
	}
	if _, err := m.Start("6.18.1"); err != nil {
		t.Fatal("completed version may rebuild:", err)
	}
}

func TestVerifyConfig(t *testing.T) {
	var body string
	for _, f := range RequiredFamilies {
		body += f + "=y\n"
	}
	if missing := VerifyConfig(body); len(missing) != 0 {
		t.Fatal("full config must pass:", missing)
	}
	if missing := VerifyConfig("CONFIG_VIRTIO_NET=y\n"); len(missing) == 0 {
		t.Fatal("sparse config must fail")
	}
	_ = strings.Join(missingStrings(), ",")
}

func missingStrings() []string { return VerifyConfig("") }

func TestTrainValidate(t *testing.T) {
	if err := (Train{Version: "5.10.0", SourceURL: "x"}).Validate(); err == nil {
		t.Fatal("non-pinned series must fail")
	}
	if err := (Train{Version: "6.18.2", SourceURL: "x", Compatible: true}).Validate(); err == nil {
		t.Fatal("compatible-without-rescan must fail")
	}
	if err := (Train{Version: "6.18.2", SourceURL: "x", Compatible: true, VirtioScanned: true}).Validate(); err != nil {
		t.Fatal(err)
	}
}
