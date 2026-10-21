package runtime

import "fmt"

// Mode selects the host execution adapter. The backend deliberately exposes
// one mode: direct Firecracker over a per-VM Unix API socket.
type Mode string

const (
	ModeDirect Mode = "direct"
)

// ParseMode validates configuration before the server starts.
func ParseMode(raw string) (Mode, error) {
	if raw == "" {
		return ModeDirect, nil
	}
	switch Mode(raw) {
	case ModeDirect:
		return Mode(raw), nil
	default:
		return "", fmt.Errorf("invalid runtime mode %q (want %q)", raw, ModeDirect)
	}
}

func (m Mode) String() string {
	if m == "" {
		return string(ModeDirect)
	}
	return string(m)
}
