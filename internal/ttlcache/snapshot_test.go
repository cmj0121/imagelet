package ttlcache

import (
	"bytes"
	"errors"
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
