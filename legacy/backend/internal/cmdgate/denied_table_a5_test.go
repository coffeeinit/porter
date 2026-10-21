package cmdgate

import "testing"

// Hermetic table over the in-code denylist: destructive patterns are denied
// without inference, everything else passes through to judgment.
func TestA5DeniedInCodeTable(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		denied  bool
		pattern string
	}{
		{"rm root", []string{"rm", "-rf", "/"}, true, "rm -rf /"},
		{"rm root glob", []string{"sudo", "rm", "-rf", "/*"}, true, "rm -rf /*"},
		{"rm case-insensitive", []string{"RM", "-RF", "/"}, true, "rm -rf /"},
		{"fork bomb", []string{":(){:|:&};:"}, true, ":(){:|:&};:"},
		{"mkfs", []string{"mkfs", "-t", "ext4", "/dev/sda1"}, true, "mkfs.* /dev/"},
		{"dd to device", []string{"dd", "of=/dev/sda"}, true, "of=/dev/"},
		{"dd with status flag", []string{"dd", "of=/dev/sda", "status=progress"}, true, "of=/dev/"},
		{"shutdown", []string{"shutdown", "-h", "now"}, true, "shutdown"},
		{"reboot", []string{"reboot"}, true, "reboot"},
		{"halt", []string{"halt"}, true, "halt"},
		{"poweroff", []string{"poweroff"}, true, "poweroff"},

		{"ls", []string{"ls", "-la"}, false, ""},
		{"cat log", []string{"cat", "/var/log/app.log"}, false, ""},
		{"ps", []string{"ps", "aux"}, false, ""},
		{"restart app", []string{"systemctl", "restart", "app"}, false, ""},
		{"echo hello", []string{"echo", "hello"}, false, ""},
		{"empty argv", nil, false, ""},
		{"rm without slash", []string{"rm", "-rf", "./build"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pattern, bad := DeniedInCode(tc.argv)
			if bad != tc.denied {
				t.Fatalf("DeniedInCode(%q) denied=%v, want %v", tc.argv, bad, tc.denied)
			}
			if tc.denied && pattern != tc.pattern {
				t.Fatalf("DeniedInCode(%q) pattern=%q, want %q", tc.argv, pattern, tc.pattern)
			}
			if !tc.denied && pattern != "" {
				t.Fatalf("DeniedInCode(%q) pattern must be empty, got %q", tc.argv, pattern)
			}
		})
	}
}
