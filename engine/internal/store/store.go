// Package store is the engine's "S3": shared, durable storage for pack blobs
// and the write-ahead log. Everything above this package (wal, coordinator)
// relies only on the contract below, so a different backend (a real S3 API)
// can replace LocalDirStore later without touching them.
package store

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"toyengine/internal/model"
)

// Store contract. Implementations must satisfy contract_test.go.
//
// Blobs: immutable, keyed by a relative path like "packs/<sha>.pack".
// Log:   a sequence of immutable Entries in numbered slots 1, 2, 3, ...
type Store interface {
	// PutBlob durably stores the bytes read from r under key.
	//  - Atomic: readers never observe a partially written blob.
	//  - Durable: when PutBlob returns nil the data survives a crash.
	//  - Idempotent: storing the same key again is not an error (keys are
	//    content addressed, so the bytes are the same; keep the first or
	//    overwrite atomically).
	PutBlob(key string, r io.Reader) error

	// GetBlob opens a stored blob. A missing key must return an error for
	// which errors.Is(err, fs.ErrNotExist) is true.
	GetBlob(key string) (io.ReadCloser, error)

	// Head returns the highest slot number present in the log (0 for an empty
	// log). Anything that is not a complete, well-formed entry (temp files) must be ignored.
	Head() (uint64, error)

	// ReadEntry returns entry seq. Missing => errors.Is(err, fs.ErrNotExist).
	ReadEntry(seq uint64) (model.Entry, error)

	// TryAppend is the compare-and-swap operation to claim slot e.Seq for e.
	//  - If the slot is already taken: return (false, nil). The existing
	//    entry must be left untouched.
	//  - If it wins: return (true, nil) only after the entry is durable AND
	//    fully visible. A concurrent reader must never see a half-written
	//    entry, and of N racing callers for the same slot exactly one wins.
	//  - Failed attempts must not leave junk files behind in the log directory.
	//
	// The store does not check that e.Seq is Head()+1: callers always claim Head()+1.
	TryAppend(e model.Entry) (won bool, err error)
}

// SlotName is the file name of log entry seq: 00000001.json, 00000002.json, ...
func SlotName(seq uint64) string { return fmt.Sprintf("%08d.json", seq) }

// ParseSlotName is the inverse of SlotName. It accepts only exactly 8 digits
// followed by ".json", and returns ok=false for anything else.
func ParseSlotName(name string) (seq uint64, ok bool) {
	digits, found := strings.CutSuffix(name, ".json")
	if !found || len(digits) != 8 {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}
