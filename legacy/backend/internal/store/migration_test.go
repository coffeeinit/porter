package store

import (
	"strings"
	"testing"

	"porter/migrations"
)

func TestEmbeddedMigrationInventory(t *testing.T) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	ups := map[string]bool{}
	downs := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			ups[strings.TrimSuffix(name, ".up.sql")] = true
			body, readErr := migrations.FS.ReadFile(name)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(splitSQL(string(body))) == 0 {
				t.Errorf("migration %s contains no executable SQL", name)
			}
		case strings.HasSuffix(name, ".down.sql"):
			downs[strings.TrimSuffix(name, ".down.sql")] = true
		}
	}
	if len(ups) == 0 {
		t.Fatal("no embedded up migrations found")
	}
	for version := range ups {
		if !downs[version] {
			t.Errorf("migration %s has no down migration", version)
		}
	}
}
