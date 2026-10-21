package deployment

import "testing"

func TestValidatePush(t *testing.T) {
	ok := PushRequest{ServiceID: "web", BaseRev: "r12", Env: map[string]string{"PORT": "8080"}}
	if err := ValidatePush(ok, "r12"); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePush(ok, "r13"); err == nil {
		t.Fatal("stale base must fail")
	}
	empty := PushRequest{ServiceID: "web", BaseRev: "r12"}
	if err := ValidatePush(empty, "r12"); err == nil {
		t.Fatal("empty change must fail")
	}
}
