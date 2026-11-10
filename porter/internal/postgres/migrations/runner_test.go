package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"testing/fstest"
)

func TestLoadMigrationsComputesDeterministicChecksum(t *testing.T) {
	body := []byte("SELECT 1;\n")
	files := fstest.MapFS{
		"0001_core.sql": &fstest.MapFile{Data: body},
	}
	loaded, err := loadMigrations(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].version != 1 || loaded[0].name != "0001_core.sql" {
		t.Fatalf("loaded migration = %+v", loaded)
	}
	digest := sha256.Sum256(body)
	if loaded[0].checksum != hex.EncodeToString(digest[:]) {
		t.Fatalf("checksum = %q, want SHA-256 %x", loaded[0].checksum, digest)
	}
}

func TestLoadMigrationsRejectsInvalidSequenceAndDownFiles(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
	}{
		{
			name: "missing initial version",
			files: fstest.MapFS{
				"0002_next.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			},
		},
		{
			name: "version gap",
			files: fstest.MapFS{
				"0001_core.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
				"0003_later.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
			},
		},
		{
			name: "down migration",
			files: fstest.MapFS{
				"0001_core.down.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
		},
		{
			name: "unversioned file",
			files: fstest.MapFS{
				"README.md": &fstest.MapFile{Data: []byte("not a migration")},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadMigrations(test.files); err == nil {
				t.Fatal("loadMigrations succeeded, want a validation error")
			}
		})
	}
}

func TestValidateAppliedRejectsChecksumDrift(t *testing.T) {
	available := []migration{{version: 1, name: "0001_core.sql", checksum: "current"}}
	applied := []appliedMigration{{version: 1, name: "0001_core.sql", checksum: "previous"}}
	if err := validateApplied(applied, available); !errors.Is(err, ErrChecksumDrift) {
		t.Fatalf("validateApplied error = %v, want ErrChecksumDrift", err)
	}
}

func TestValidateAppliedRejectsIncompatibleHistory(t *testing.T) {
	available := []migration{
		{version: 1, name: "0001_core.sql", checksum: "one"},
		{version: 2, name: "0002_next.sql", checksum: "two"},
	}
	applied := []appliedMigration{{version: 2, name: "0002_next.sql", checksum: "two"}}
	if err := validateApplied(applied, available); !errors.Is(err, ErrMigrationHistory) {
		t.Fatalf("validateApplied error = %v, want ErrMigrationHistory", err)
	}
}

func TestApplyRejectsNilDatabase(t *testing.T) {
	if err := Apply(context.Background(), nil); err == nil {
		t.Fatal("Apply(nil) succeeded, want an error")
	}
}
