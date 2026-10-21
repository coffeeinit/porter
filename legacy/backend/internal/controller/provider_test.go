package controller

import (
	"context"
	"testing"
)

func TestStaticProvider(t *testing.T) {
	var p Provider = StaticProvider{}
	if p.Name() != "static" {
		t.Fatalf("bad name %q", p.Name())
	}
	exists, err := p.InstanceExists(context.Background(), "n1")
	if err != nil || exists {
		t.Fatal("static existence must be unknown (false, nil)")
	}
}
