package ai

import "testing"

func TestMCPAddress(t *testing.T) {
	ws, kind, name, err := ParseAddress("wi.acme.github.main")
	if err != nil || ws != "acme" || kind != "github" || name != "main" {
		t.Fatalf("bad parse: %v %q %q %q", err, ws, kind, name)
	}
	for _, bad := range []string{"", "wi.acme", "xx.acme.github.main", "wi.acme..main"} {
		if _, _, _, err := ParseAddress(bad); err == nil {
			t.Fatalf("must fail: %q", bad)
		}
	}
}

func TestMCPAuthorize(t *testing.T) {
	open := Connector{Address: "wi.acme.github.main", Policy: PolicyAllow}
	if err := open.Authorize(Call{Address: open.Address}); err != nil {
		t.Fatal(err)
	}
	gated := Connector{Address: "wi.acme.billing.prod", Policy: PolicyRequireApproval}
	if err := gated.Authorize(Call{Address: gated.Address}); err == nil {
		t.Fatal("unapproved gated call must fail")
	}
	ok := Call{Address: gated.Address, Approved: true, ApprovedBy: "op-1"}
	if err := gated.Authorize(ok); err != nil {
		t.Fatal(err)
	}
	if err := open.Authorize(Call{Address: "wi.acme.github.other"}); err == nil {
		t.Fatal("address mismatch must fail")
	}
}
