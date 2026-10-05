package runtime

import (
	goruntime "runtime"
	"testing"
)

func TestMicroVMHostMatchesGOOS(t *testing.T) {
	if MicroVMHost() != (goruntime.GOOS == "linux") {
		t.Fatalf("MicroVMHost must be true only on linux, GOOS=%s", goruntime.GOOS)
	}
}

func TestRequireMicroVMHostExplicitOffLinux(t *testing.T) {
	err := RequireMicroVMHost("test boot")
	if goruntime.GOOS == "linux" && err != nil {
		t.Fatalf("linux must allow boots: %v", err)
	}
	if goruntime.GOOS != "linux" && err == nil {
		t.Fatal("non-linux must refuse boots with an explicit error")
	}
}
