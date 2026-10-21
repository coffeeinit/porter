package gateway

import "testing"

func TestRouteSignVerify(t *testing.T) {
	key := []byte("32-byte-test-key-0123456789abcdef")
	sig := SignRoute(key, "m-web.example.com", "node1:8080")
	if !VerifyRoute(key, "m-web.example.com", "node1:8080", sig) {
		t.Fatal("valid signature must verify")
	}
	if VerifyRoute(key, "m-web.example.com", "node2:8080", sig) {
		t.Fatal("backend swap must not verify")
	}
	if VerifyRoute(nil, "h", "b", sig) {
		t.Fatal("empty key must fail closed")
	}
}

func TestDecide(t *testing.T) {
	if got := Decide("Googlebot/2.1", true); got != EdgePrerender {
		t.Fatal("bot must prerender")
	}
	if got := Decide("Mozilla/5.0", true); got != EdgeApp {
		t.Fatal("user must get app")
	}
	if got := Decide("Mozilla/5.0", false); got != EdgeResolve {
		t.Fatal("unsigned must re-resolve")
	}
}
