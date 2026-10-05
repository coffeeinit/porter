package runtime

import "testing"

func TestParseModeDefaultsToDirect(t *testing.T) {
	got, err := ParseMode("")
	if err != nil {
		t.Fatalf("ParseMode(\"\"): %v", err)
	}
	if got != ModeDirect {
		t.Fatalf("ParseMode(\"\") = %q, want %q", got, ModeDirect)
	}
}

func TestParseModeAcceptsKnownModes(t *testing.T) {
	for _, want := range []Mode{ModeDirect} {
		got, err := ParseMode(string(want))
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", want, err)
		}
		if got != want {
			t.Fatalf("ParseMode(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestParseModeRejectsUnknownMode(t *testing.T) {
	if _, err := ParseMode("mystery"); err == nil {
		t.Fatal("ParseMode(mystery) returned nil error")
	}
}
