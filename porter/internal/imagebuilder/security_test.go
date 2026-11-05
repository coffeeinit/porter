package imagebuilder

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveBudgetEnforcesEntryAndExpandedByteLimits(t *testing.T) {
	b := &archiveBudget{entries: maxArchiveEntries}
	if err := b.addEntry(0); err == nil {
		t.Fatal("expected entry limit rejection")
	}
	b = &archiveBudget{bytes: maxExpandedBytes - 1}
	if err := b.addEntry(2); err == nil {
		t.Fatal("expected expanded byte limit rejection")
	}
}

func TestCopyWithContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyWithContext(ctx, &bytes.Buffer{}, bytes.NewReader([]byte("payload"))); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestApplyLayerRejectsSymlinkDestination(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "root")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(d, "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "target")); err != nil {
		t.Fatal(err)
	}
	p := makeLayer(t, d, []tar.Header{{Name: "target", Typeflag: tar.TypeReg, Mode: 0644}})
	if err := applyLayer(p, root); err == nil {
		t.Fatal("expected symlink destination rejection")
	}
	b, err := os.ReadFile(outside)
	if err != nil || string(b) != "untouched" {
		t.Fatalf("outside target changed: %q, %v", b, err)
	}
}

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

func TestApplyLayerHonorsCancellationBeforeProcessing(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "root")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	p := makeLayer(t, d, []tar.Header{{Name: "dir", Typeflag: tar.TypeDir, Size: 32 * 1024 * 1024}, {Name: "later", Typeflag: tar.TypeReg, Mode: 0644}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applyLayerContext(ctx, p, root, &archiveBudget{}); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

type cancelingReader struct {
	cancel context.CancelFunc
	done   bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	r.cancel()
	return copy(p, []byte("payload")), nil
}

func TestDrainTarEntryHonorsCancellationDuringDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelingReader{cancel: cancel}
	if err := drainTarEntry(ctx, r); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestReadMetadataLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), maxMetadataBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMetadata(p); err == nil {
		t.Fatal("expected oversized metadata rejection")
	}
}

func TestVerifyBlobContextStreamsLargeBlob(t *testing.T) {
	p := filepath.Join(t.TempDir(), "large-layer.blob")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	block := bytes.Repeat([]byte("z"), 1024*1024)
	for i := 0; i < 20; i++ {
		if _, err := f.Write(block); err != nil {
			t.Fatal(err)
		}
		_, _ = h.Write(block)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if err := verifyBlobContext(context.Background(), p, digest); err != nil {
		t.Fatal(err)
	}
}
