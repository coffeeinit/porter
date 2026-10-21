package storage

import (
	"testing"
	"time"
)

func TestSnapshotPair(t *testing.T) {
	files := []string{
		"snapshot-vm1-vmstate-20260101-120000.fc",
		"snapshot-vm1-memfile-20260101-120000.fc",
		"snapshot-vm1-vmstate-20260102-120000.fc",
		"snapshot-vm1-memfile-20260102-120000.fc",
		"snapshot-vm1-vmstate-20260103-120000.fc", // orphan, no memfile
		"random.txt",
	}
	got := ParseSnapshotPair(files)
	if len(got) != 2 {
		t.Fatalf("want 2 pairs, got %d", len(got))
	}
	if !got[0].Timestamp.After(got[1].Timestamp) {
		t.Fatal("newest first")
	}
	s, m := FileNames("vm1", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))
	if s != "snapshot-vm1-vmstate-20260102-120000.fc" || m != "snapshot-vm1-memfile-20260102-120000.fc" {
		t.Fatalf("bad names %q %q", s, m)
	}
}

func TestSnapshotPhases(t *testing.T) {
	p, err := NextCreatePhase(PhasePausing, nil)
	if err != nil || p != PhaseSnapshotted {
		t.Fatal("pause->snapshot")
	}
	p, err = NextCreatePhase(PhaseSnapshotted, errBoom())
	if err == nil || p != PhaseResuming {
		t.Fatal("snapshot error must still resume, carrying the error")
	}
	p, err = NextCreatePhase(PhaseResuming, nil)
	if err != nil || p != PhaseDone {
		t.Fatal("resume->done")
	}
	if _, err := NextCreatePhase(PhaseDone, nil); err == nil {
		t.Fatal("terminal phase must fail")
	}
}

func errBoom() error { return errTest{} }

type errTest struct{}

func (errTest) Error() string { return "boom" }

func TestManifestValidate(t *testing.T) {
	ok := Manifest{APIVersion: "porter.storage/v1", VMName: "web",
		Checksums: map[string]string{"rootfs.ext4": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := Manifest{APIVersion: "porter.storage/v1", VMName: "web",
		Checksums: map[string]string{"rootfs.ext4": "md5hash"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("non-sha256 must fail")
	}
}

func TestRetain(t *testing.T) {
	mk := func(id string, day int, v bool) Backup {
		return Backup{ID: id, CreatedAt: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC), Verified: v}
	}
	bs := []Backup{mk("a", 1, true), mk("b", 2, false), mk("c", 3, true), mk("d", 4, true)}
	keep, drop := Retain(bs, 2)
	if len(keep) != 2 || len(drop) != 2 {
		t.Fatalf("keep=%v drop=%v", keep, drop)
	}
	for _, b := range keep {
		if !b.Verified {
			t.Fatalf("verified preferred, kept %v", b.ID)
		}
	}
	if got := ObjectPath("web", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)); got != "backups/web/20260102-030405.ext4.zst.enc" {
		t.Fatalf("bad object path %q", got)
	}
}
