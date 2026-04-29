package ttlcache

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sample struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// TestSaveLoadJSONRoundtrips pins the load-after-save path: every
// entry written by SaveJSON shows up in a fresh cache after LoadJSON.
func TestSaveLoadJSONRoundtrips(t *testing.T) {
	src := New[string, sample](16)
	exp := time.Now().Add(time.Hour)
	src.Set("a", Entry[sample]{Value: sample{Name: "alpha", Count: 1}, Found: true, ExpiresAt: exp})
	src.Set("b", Entry[sample]{Value: sample{Name: "beta", Count: 2}, Found: false, ExpiresAt: exp}) // negative cache

	var buf bytes.Buffer
	if err := SaveJSON(src, &buf); err != nil {
		t.Fatalf("SaveJSON: %v", err)
	}

	dst := New[string, sample](16)
	if err := LoadJSON(dst, &buf); err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}

	for _, k := range []string{"a", "b"} {
		got, ok := dst.Get(k)
		want, _ := src.Get(k)
		if !ok {
			t.Errorf("entry %q lost in roundtrip", k)
			continue
		}
		if got.Value != want.Value || got.Found != want.Found {
			t.Errorf("entry %q: got %+v, want %+v", k, got, want)
		}
	}
}

// TestLoadJSONSkipsExpiredEntries pins that a stale snapshot doesn't
// resurrect entries past their TTL — protects against a long-stopped
// process leaving cached values that look fresh after restart.
func TestLoadJSONSkipsExpiredEntries(t *testing.T) {
	src := New[string, sample](16)
	src.Set("alive", Entry[sample]{Value: sample{Name: "alive"}, Found: true, ExpiresAt: time.Now().Add(time.Hour)})
	// expired-on-save: All() filters these out, so this won't even
	// land in the snapshot. Verify by setting one with a past
	// expiry — it should be filtered when reading back too.
	src.Set("dead", Entry[sample]{Value: sample{Name: "dead"}, Found: true, ExpiresAt: time.Now().Add(-time.Hour)})

	var buf bytes.Buffer
	if err := SaveJSON(src, &buf); err != nil {
		t.Fatalf("SaveJSON: %v", err)
	}

	dst := New[string, sample](16)
	if err := LoadJSON(dst, &buf); err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	if _, ok := dst.Get("alive"); !ok {
		t.Error("live entry should survive roundtrip")
	}
	if _, ok := dst.Get("dead"); ok {
		t.Error("expired entry should not resurrect")
	}
}

// TestLoadJSONSchemaMismatchReturnsErr pins the schema-versioning
// safety net: a snapshot from a different schema returns
// ErrSchemaMismatch so the caller can drop the file rather than
// unmarshaling into silent zeros.
func TestLoadJSONSchemaMismatchReturnsErr(t *testing.T) {
	stale := []byte(`{"schema": 99, "saved_at": "2026-04-29T00:00:00Z", "entries": []}`)
	dst := New[string, sample](16)
	err := LoadJSON(dst, bytes.NewReader(stale))
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("err = %v, want ErrSchemaMismatch", err)
	}
}

// TestSaveJSONFileAtomicWrite pins the tempfile + rename behaviour:
// after SaveJSONFile returns the target file holds the full snapshot
// AND no .tmp file is left behind.
func TestSaveJSONFileAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "snap.json")

	src := New[string, sample](16)
	src.Set("k", Entry[sample]{Value: sample{Name: "v"}, Found: true, ExpiresAt: time.Now().Add(time.Hour)})

	if err := SaveJSONFile(src, path); err != nil {
		t.Fatalf("SaveJSONFile: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("target file missing: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file should be gone after rename, err = %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !strings.Contains(string(body), `"schema": 1`) {
		t.Errorf("snapshot missing schema header: %s", body)
	}
}

// TestLoadJSONFileMissingIsNoop pins the no-snapshot-yet startup path:
// a fresh process with an empty --cache-dir should not error on the
// first run; it should just continue with a cold cache.
func TestLoadJSONFileMissingIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.json")

	dst := New[string, sample](16)
	if err := LoadJSONFile(dst, path); err != nil {
		t.Errorf("missing file should be no-op, got err = %v", err)
	}
	if dst.Len() != 0 {
		t.Errorf("Len = %d, want 0 on missing-file load", dst.Len())
	}
}

// TestLoadJSONRefusesOversize pins the OOM-prevention guard: a
// snapshot stream that exceeds MaxSnapshotBytes must surface
// ErrSnapshotTooLarge instead of being decoded into memory.
func TestLoadJSONRefusesOversize(t *testing.T) {
	orig := MaxSnapshotBytes
	MaxSnapshotBytes = 1024
	defer func() { MaxSnapshotBytes = orig }()

	src := New[string, sample](128)
	exp := time.Now().Add(time.Hour)
	// 50 entries with long keys easily exceed 1 KiB of JSON.
	for i := 0; i < 50; i++ {
		k := fmt.Sprintf("padded-key-%030d", i)
		src.Set(k, Entry[sample]{Value: sample{Name: k, Count: i}, Found: true, ExpiresAt: exp})
	}

	var buf bytes.Buffer
	if err := SaveJSON(src, &buf); err != nil {
		t.Fatalf("SaveJSON: %v", err)
	}
	if int64(buf.Len()) <= MaxSnapshotBytes {
		t.Fatalf("test setup: snapshot %d bytes <= cap %d, oversize check is moot", buf.Len(), MaxSnapshotBytes)
	}

	dst := New[string, sample](128)
	err := LoadJSON(dst, &buf)
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Errorf("err = %v, want ErrSnapshotTooLarge", err)
	}
}

// TestSaveJSONFileRefusesSymlinkDest pins the TOCTOU guard: if the
// destination path has been swapped to a symlink between MkdirAll
// and Rename, SaveJSONFile must refuse rather than clobber the
// symlink target.
func TestSaveJSONFileRefusesSymlinkDest(t *testing.T) {
	dir := t.TempDir()
	// Real file outside the snapshot dir, holding sentinel content.
	realPath := filepath.Join(dir, "victim.txt")
	sentinel := []byte("untouched")
	if err := os.WriteFile(realPath, sentinel, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	snapPath := filepath.Join(dir, "snap.json")
	if err := os.Symlink(realPath, snapPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	src := New[string, sample](16)
	src.Set("k", Entry[sample]{Value: sample{Name: "v"}, Found: true, ExpiresAt: time.Now().Add(time.Hour)})

	err := SaveJSONFile(src, snapPath)
	if err == nil {
		t.Fatalf("SaveJSONFile: want error, got nil")
	}
	if !strings.Contains(err.Error(), "refuse to rename over symlink") {
		t.Errorf("err = %v, want substring %q", err, "refuse to rename over symlink")
	}

	got, readErr := os.ReadFile(realPath)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if !bytes.Equal(got, sentinel) {
		t.Errorf("victim file content changed: got %q, want %q", got, sentinel)
	}
}

// TestSaveJSONFilePermissions pins that the snapshot file lands at
// 0o600 and its parent directory at 0o700 — cached upstream payloads
// stay readable only by the running UID.
func TestSaveJSONFilePermissions(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "sub", "dir")
	path := filepath.Join(parent, "snap.json")

	src := New[string, sample](16)
	src.Set("k", Entry[sample]{Value: sample{Name: "v"}, Found: true, ExpiresAt: time.Now().Add(time.Hour)})

	if err := SaveJSONFile(src, path); err != nil {
		t.Fatalf("SaveJSONFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if got := fi.Mode() & os.ModePerm; got != 0o600 {
		t.Errorf("file mode = %o, want 0o600", got)
	}

	di, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := di.Mode() & os.ModePerm; got != 0o700 {
		t.Errorf("dir mode = %o, want 0o700", got)
	}
}
