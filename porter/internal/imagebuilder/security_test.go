package imagebuilder

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeLayer(t *testing.T, root string, entries []tar.Header) string {
	t.Helper()
	p := filepath.Join(root, "layer.tar")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	tw := tar.NewWriter(f)
	for _, original := range entries {
		h := original
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
			h.Size = int64(len("payload"))
		}
		if e = tw.WriteHeader(&h); e != nil {
			t.Fatal(e)
		}
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
			if _, e = tw.Write([]byte("payload")); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestExtractArchiveRejectsTraversalAndAbsolute(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute"} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			p := makeLayer(t, d, []tar.Header{{Name: name, Typeflag: tar.TypeReg, Mode: 0644}})
			if err := extractArchive(p, filepath.Join(d, "out")); err == nil {
				t.Fatal("expected unsafe path rejection")
			}
		})
	}
}
func TestApplyLayerRejectsTraversalSymlinkParentAndUnsafeTarget(t *testing.T) {
	tests := []struct {
		name    string
		headers []tar.Header
	}{
		{"dotdot", []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0644}}},
		{"absolute", []tar.Header{{Name: "/escape", Typeflag: tar.TypeReg, Mode: 0644}}},
		{"symlink-parent", []tar.Header{{Name: "pivot", Typeflag: tar.TypeSymlink, Linkname: "inside"}, {Name: "pivot/file", Typeflag: tar.TypeReg, Mode: 0644}}},
		{"unsafe-link", []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"}}},
		{"hard-link", []tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := t.TempDir()
			p := makeLayer(t, d, tt.headers)
			root := filepath.Join(d, "root")
			os.Mkdir(root, 0755)
			if err := applyLayer(p, root); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
func TestBlobPathRejectsMalformedDigest(t *testing.T) {
	for _, d := range []string{"sha256:xyz", "sha256:" + hex.EncodeToString(bytes.Repeat([]byte{1}, 31)), "sha1:" + hex.EncodeToString(make([]byte, 32))} {
		if _, err := blobPath(t.TempDir(), d); err == nil {
			t.Fatalf("accepted %q", d)
		}
	}
}
func TestReadBlobVerifiesDigest(t *testing.T) {
	d := t.TempDir()
	b := []byte("content")
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	p, _ := blobPath(d, digest)
	os.MkdirAll(filepath.Dir(p), 0755)
	os.WriteFile(p, []byte("tampered"), 0644)
	if _, err := readBlob(d, digest); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestSafeArtifactNameConfinesBuildPaths(t *testing.T) {
	for _, input := range []string{"../escape", "/tmp/escape", "../../image:tag", "..", ""} {
		name := safeArtifactName(input)
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
			t.Errorf("safeArtifactName(%q) = %q, not a safe filename component", input, name)
		}
	}
}
