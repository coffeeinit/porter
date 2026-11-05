package gateway

import "testing"

func TestNormalizeHost(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"Example.COM:443", "example.com"},
		{"EXAMPLE.com.", "example.com"},
		{"[2001:db8::1]:8080", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
	} {
		if got := normalizeHost(tc.input); got != tc.want {
			t.Errorf("normalizeHost(%q)=%q want %q", tc.input, got, tc.want)
		}
	}
}

func TestStripPathPrefix(t *testing.T) {
	for _, tc := range []struct {
		path, prefix, want string
		ok                 bool
	}{
		{"/api/users", "/api", "/users", true},
		{"/api", "/api", "/", true},
		{"/api/", "/api/", "/", true},
		{"/apix", "/api", "", false},
		{"/other", "/api", "", false},
		{"/anything", "/", "/anything", true},
		{"/anything", "", "/anything", true},
		{"/api", "api", "", false},
	} {
		got, ok := stripPathPrefix(tc.path, tc.prefix)
		if got != tc.want || ok != tc.ok {
			t.Errorf("stripPathPrefix(%q,%q)=(%q,%v) want (%q,%v)", tc.path, tc.prefix, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidTraceID(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"trace-01.a_b", true},
		{"", false},
		{"bad id", false},
		{"bad\nheader", false},
		{string(make([]byte, 129)), false},
	} {
		if got := validTraceID(tc.value); got != tc.want {
			t.Errorf("validTraceID(%q)=%v want %v", tc.value, got, tc.want)
		}
	}
}
