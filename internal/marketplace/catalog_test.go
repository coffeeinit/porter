package marketplace

import "testing"

func TestItemValidate(t *testing.T) {
	ok := Item{Publisher: "porter", Name: "postgres", Version: "16.2",
		Digest:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ManifestURI: "s3://catalog/postgres-16.2.json"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.ID() != "porter/postgres" {
		t.Fatalf("bad id %q", ok.ID())
	}
	bad := ok
	bad.Digest = "md5"
	if err := bad.Validate(); err == nil {
		t.Fatal("non-sha256 digest must fail")
	}
}

func TestFetchPlan(t *testing.T) {
	parts := []Part{{Filename: "c"}, {Filename: "a"}, {Filename: "b"}}
	got := FetchPlan(parts)
	if got[0].Filename != "a" || got[1].Filename != "b" || got[2].Filename != "c" {
		t.Fatalf("plan must sort: %v", got)
	}
	if parts[0].Filename != "c" {
		t.Fatal("plan must not mutate input")
	}
}
