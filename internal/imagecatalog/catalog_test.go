package imagecatalog

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewLoadsAndSortsByName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.json"), `{"id":"b","name":"Bravo","image":"ghcr.io/x/b"}`)
	writeFile(t, filepath.Join(dir, "a.json"), `{"id":"a","name":"Alpha","image":"ghcr.io/x/a"}`)
	// a non-JSON file and a directory are ignored
	writeFile(t, filepath.Join(dir, "notes.txt"), "not json")
	if err := os.MkdirAll(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := New(dir)
	all := c.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 images, got %d", len(all))
	}
	if all[0].Name != "Alpha" || all[1].Name != "Bravo" {
		t.Fatalf("expected sorted by name, got %+v", all)
	}
}

func TestNewDerivesIDFromFilename(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "redis.json"), `{"name":"Redis","image":"docker.io/library/redis"}`)
	c := New(dir)
	all := c.All()
	if len(all) != 1 || all[0].ID != "redis" {
		t.Fatalf("expected id derived from filename, got %+v", all)
	}
}

func TestNewMissingDirIsEmpty(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "nope"))
	if len(c.All()) != 0 {
		t.Fatal("expected empty catalog for missing dir")
	}
}

func TestNewEmptyDirIsEmpty(t *testing.T) {
	c := New(t.TempDir())
	if len(c.All()) != 0 {
		t.Fatal("expected empty catalog for empty dir")
	}
}
