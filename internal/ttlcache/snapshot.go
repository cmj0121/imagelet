package ttlcache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// snapshotSchema is the on-disk JSON schema version. Bumped whenever
// the persisted Entry / snapshot structure changes in a way that
// requires existing files to be discarded — e.g. a Cache value type
// adding a non-optional field. LoadJSON returns ErrSchemaMismatch when
// the file was written under a different schema, letting the caller
// drop the file and start cold instead of unmarshaling silent zeros.
const snapshotSchema = 1

// ErrSchemaMismatch reports that the snapshot file's schema field
// doesn't match snapshotSchema. Callers typically log + delete the
// stale file so the next save writes a current-schema replacement.
var ErrSchemaMismatch = errors.New("ttlcache: snapshot schema mismatch")

// snapshot is the JSON envelope written by SaveJSON. The Schema field
// pins backwards-compatibility so a value-type change can invalidate
// stale snapshots gracefully (LoadJSON returns ErrSchemaMismatch).
type snapshot[V any] struct {
	Schema  int                `json:"schema"`
	SavedAt time.Time          `json:"saved_at"`
	Entries []snapshotEntry[V] `json:"entries"`
}

type snapshotEntry[V any] struct {
	Key       string    `json:"key"`
	Value     V         `json:"value"`
	Found     bool      `json:"found"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SaveJSON writes c's live (unexpired) entries to w as a JSON
// snapshot. K is constrained to string because JSON object keys are
// strings — non-string-keyed caches would need a custom marshaler
// and aren't expected in this codebase. V's exported fields are
// serialized via encoding/json.
func SaveJSON[V any](c *Cache[string, V], w io.Writer) error {
	all := c.All()
	snap := snapshot[V]{
		Schema:  snapshotSchema,
		SavedAt: time.Now(),
		Entries: make([]snapshotEntry[V], 0, len(all)),
	}
	for k, e := range all {
		snap.Entries = append(snap.Entries, snapshotEntry[V]{
			Key:       k,
			Value:     e.Value,
			Found:     e.Found,
			ExpiresAt: e.ExpiresAt,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snap)
}

// LoadJSON populates c with entries from r — a JSON snapshot
// previously written by SaveJSON. Already-expired entries are
// skipped so a long-stored file doesn't resurrect stale data.
// Returns ErrSchemaMismatch if the file was written under a
// different schema; the caller should drop the file in that case.
func LoadJSON[V any](c *Cache[string, V], r io.Reader) error {
	var snap snapshot[V]
	if err := json.NewDecoder(r).Decode(&snap); err != nil {
		return fmt.Errorf("ttlcache: decode snapshot: %w", err)
	}
	if snap.Schema != snapshotSchema {
		return fmt.Errorf("%w: file=%d, want=%d", ErrSchemaMismatch, snap.Schema, snapshotSchema)
	}
	now := time.Now()
	for _, e := range snap.Entries {
		if !e.ExpiresAt.IsZero() && !e.ExpiresAt.After(now) {
			continue
		}
		c.Set(e.Key, Entry[V]{
			Value:     e.Value,
			Found:     e.Found,
			ExpiresAt: e.ExpiresAt,
		})
	}
	return nil
}

// SaveJSONFile writes c to a file at path atomically: the JSON is
// first written to "<path>.tmp" then renamed in place, so a partial
// write (e.g. process killed mid-flush) never replaces the existing
// file with a truncated version. Creates the parent directory if
// missing.
func SaveJSONFile[V any](c *Cache[string, V], path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ttlcache: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("ttlcache: open tmpfile: %w", err)
	}
	if err := SaveJSON(c, f); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("ttlcache: close tmpfile: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("ttlcache: rename: %w", err)
	}
	return nil
}

// LoadJSONFile reads a snapshot from path and populates c. Missing
// files are treated as a no-op (no error) so a cold start with no
// snapshot directory yet just continues. Schema mismatches log via
// the returned ErrSchemaMismatch — the caller decides whether to
// delete the stale file.
func LoadJSONFile[V any](c *Cache[string, V], path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("ttlcache: open: %w", err)
	}
	defer f.Close()
	return LoadJSON(c, f)
}
