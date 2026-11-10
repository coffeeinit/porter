package store

import (
	"context"
	"errors"
	"testing"

	v1 "porter/controlplane/contract/v1"
)

func TestNormalizeMountPath(t *testing.T) {
	for _, valid := range []string{"", "/", "/var/lib/data", "/data-1/cache_2/file.ext"} {
		got, err := normalizeMountPath(valid)
		if err != nil || got != valid {
			t.Errorf("normalizeMountPath(%q) = %q, %v", valid, got, err)
		}
	}
	for _, invalid := range []string{"relative/path", "/../escape", "/a/./b", "/a//b", "/trailing/", "/white space", "/encoded%2fpath", "/question?mark"} {
		if _, err := normalizeMountPath(invalid); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("normalizeMountPath(%q) error = %v, want ErrInvalidArgument", invalid, err)
		}
	}
}

func TestStorageMetadataInputsFailClosedBeforeDatabaseAccess(t *testing.T) {
	store := &Store{}
	if _, err := store.CreateVolume(context.Background(), "prj_a", "svc_a", v1.CreateVolumeRequest{
		Name: "data", SizeMiB: 1024, AccessMode: "shared_writable",
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("shared-writable volume error = %v, want ErrInvalidArgument", err)
	}
	if _, err := store.CreateSnapshotMetadata(context.Background(), "ins_a", "application_consistent"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("application-consistent snapshot error = %v, want ErrInvalidArgument", err)
	}
	if _, err := store.CreateBackupMetadata(context.Background(), "snp_a", "application_consistent"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("application-consistent backup error = %v, want ErrInvalidArgument", err)
	}
}
