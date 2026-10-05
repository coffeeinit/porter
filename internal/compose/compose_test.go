package compose

import "testing"

func TestParseComposeBasic(t *testing.T) {
	yaml := `
services:
  api:
    image: myapp/api:latest
    ports:
      - "3000:3000"
    environment:
      - FOO=bar
    deploy:
      replicas: 3
  worker:
    image: myapp/worker:latest
    depends_on:
      - api
    restart: on-failure
`
	svcs, err := ParseCompose(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(svcs) != 2 {
		t.Fatalf("expected 2 services, got %d", len(svcs))
	}
	if svcs[0].Name != "api" || svcs[1].Name != "worker" {
		t.Fatalf("unexpected boot order: %v, %v", svcs[0].Name, svcs[1].Name)
	}
	if svcs[0].Replicas != 3 {
		t.Fatalf("expected 3 replicas for api, got %d", svcs[0].Replicas)
	}
	if svcs[0].Env["FOO"] != "bar" {
		t.Fatalf("expected env FOO=bar, got %q", svcs[0].Env["FOO"])
	}
	if len(svcs[0].Ports) != 1 || svcs[0].Ports[0].ContainerPort != 3000 {
		t.Fatalf("unexpected ports: %+v", svcs[0].Ports)
	}
}

func TestParseComposeRejectsBuild(t *testing.T) {
	yaml := "services:\n  api:\n    build: .\n"
	_, err := ParseCompose(yaml)
	if err == nil {
		t.Fatal("expected error for build: key, got nil")
	}
}

func TestParseComposeCircularDependency(t *testing.T) {
	yaml := "services:\n  a:\n    image: x\n    depends_on:\n      - b\n  b:\n    image: y\n    depends_on:\n      - a\n"
	_, err := ParseCompose(yaml)
	if err == nil {
		t.Fatal("expected circular dependency error, got nil")
	}
}
