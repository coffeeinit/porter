package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func cfStub(t *testing.T) (*httptest.Server, *CFClient) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]string{{"message": "auth"}}})
			return
		}
		ok := func(v any) {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": v})
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/ac1/cfd_tunnel":
			ok(map[string]string{"id": "tun1", "name": "porter-vm-x"})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/ac1/cfd_tunnel/tun1/configurations":
			ok(map[string]any{})
		case r.Method == http.MethodDelete && r.URL.Path == "/accounts/ac1/cfd_tunnel/tun1":
			ok(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/z1/dns_records":
			ok(map[string]string{"id": "rec1"})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			ok([]map[string]string{{"id": "z1", "name": "example.com"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/zones/z1/dns_records/rec1":
			ok(map[string]any{})
		default:
			ok(map[string]any{})
		}
	}))
	return srv, &CFClient{BaseURL: srv.URL, Token: "tok123", Account: "ac1"}
}

func TestCreateTunnel(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	rec, err := c.CreateTunnel("porter-vm-x")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "tun1" {
		t.Fatal("wrong tunnel id")
	}
	if _, err := (&CFClient{}).CreateTunnel("x"); err == nil {
		t.Fatal("missing token must fail")
	}
}

func TestUpsertCNAME(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	if err := c.UpsertCNAME("z1", "m-web.example.com", "tun1.cfargotunnel.com"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertCNAME("", "h", "t"); err == nil {
		t.Fatal("missing zone must fail")
	}
}

func TestPutIngressConfig(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	rules := []IngressRule{
		{Hostname: "m-web.example.com", Service: "http://localhost:8080"},
		{Service: "http_status:404"},
	}
	if err := c.PutIngressConfig("tun1", rules); err != nil {
		t.Fatal(err)
	}
	if err := c.PutIngressConfig("", rules); err == nil {
		t.Fatal("missing tunnel must fail")
	}
	if err := c.PutIngressConfig("tun1", nil); err == nil {
		t.Fatal("empty rules must fail")
	}
	noCatchAll := []IngressRule{
		{Hostname: "a.example.com", Service: "http://localhost:8080"},
	}
	if err := c.PutIngressConfig("tun1", noCatchAll); err == nil {
		t.Fatal("missing catch-all must fail")
	}
}

func TestDeleteTunnel(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	if err := c.DeleteTunnel("tun1"); err != nil {
		t.Fatal(err)
	}
	if err := (&CFClient{BaseURL: srv.URL}).DeleteTunnel("tun1"); err == nil {
		t.Fatal("missing token must fail")
	}
}

func TestUpsertA(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	if err := c.UpsertA("z1", "m-web.example.com", "203.0.113.7", true); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertA("z1", "h", "not-an-ip", true); err == nil {
		t.Fatal("bad ip must fail")
	}
	if err := c.UpsertA("", "h", "203.0.113.7", true); err == nil {
		t.Fatal("missing zone must fail")
	}
}

func TestUpsertTXT(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	if err := c.UpsertTXT("z1", "_acme-challenge.m.example.com", "challenge-token"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertTXT("z1", "h", ""); err == nil {
		t.Fatal("missing value must fail")
	}
}

func TestFindZoneID(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	id, err := c.FindZoneID("example.com")
	if err != nil || id != "z1" {
		t.Fatalf("zone lookup: %q %v", id, err)
	}
	id, err = c.FindZoneID("Example.COM")
	if err != nil || id != "z1" {
		t.Fatalf("case-insensitive lookup: %q %v", id, err)
	}
	if _, err := c.FindZoneID("missing.example.net"); err == nil {
		t.Fatal("unknown domain must fail")
	}
	if _, err := c.FindZoneID(""); err == nil {
		t.Fatal("empty domain must fail")
	}
}

func TestDeleteDNSRecord(t *testing.T) {
	srv, c := cfStub(t)
	defer srv.Close()
	if err := c.DeleteDNSRecord("z1", "rec1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDNSRecord("", "rec1"); err == nil {
		t.Fatal("missing zone must fail")
	}
	if err := c.DeleteDNSRecord("z1", ""); err == nil {
		t.Fatal("missing record must fail")
	}
}

func TestCFClientAuthFailure(t *testing.T) {
	srv, _ := cfStub(t)
	defer srv.Close()
	bad := &CFClient{BaseURL: srv.URL, Token: "wrong", Account: "ac1"}
	if _, err := bad.CreateTunnel("x"); err == nil {
		t.Fatal("bad token must fail")
	}
}
