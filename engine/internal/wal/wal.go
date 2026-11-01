// Package wal turns the store's raw "claim slot N" primitive into the engine's
// log semantics: validated, linearizable publication of pushes.
package wal

import (
	"errors"
	"fmt"

	"toyengine/internal/model"
	"toyengine/internal/store"
)

// ErrConflict means the push's expectations no longer hold: for some update,
// the ref's current value in the log is not the Old value the push assumed.
// In v1 (single writer per repo) it only happens when the log and the repo have diverged.
var ErrConflict = errors.New("ref update conflicts with the log")

// FoldRefs replays log entries 1..upTo and returns the resulting value of every
// ref that exists (ref name -> object id).
//
// Rules for applying an update {Ref, Old, New}:
//   - New == model.ZeroOID  => the ref is deleted (remove it from the map)
//   - otherwise             => the ref is set to New
//
// FoldRefs does not validate Old values; Publish does that before appending.
// FoldRefs(s, 0) is an empty map.
func FoldRefs(s store.Store, upTo uint64) (map[string]string, error) {
	refs := make(map[string]string)
	for seq := uint64(1); seq <= upTo; seq++ {
		entry, err := s.ReadEntry(seq)
		if err != nil {
			return nil, err
		}
		for _, u := range entry.Updates {
			if u.New == model.ZeroOID {
				delete(refs, u.Ref)
			} else {
				refs[u.Ref] = u.New
			}
		}
	}
	return refs, nil
}

// Publish appends e to the log and returns the sequence number it was given.
// This is where linearizability is enforced.
//
//   - Ignores any e.Seq the caller set; assigns Seq = (head just read) + 1 and
//     claims that slot with Store.TryAppend.
//   - Before appending, checks every update's Old value against the refs as of
//     that head (model.ZeroOID means "ref must not exist"); a mismatch returns
//     ErrConflict and appends nothing.
//   - If another writer wins the slot first, it re-reads the head and
//     RE-VALIDATES against the new state before trying again: the state the
//     first check saw is stale. It gives up only on a real conflict or a store error.
func Publish(s store.Store, e model.Entry) (uint64, error) {
	for {
		head, err := s.Head()
		if err != nil {
			return 0, err
		}
		refs, err := FoldRefs(s, head)
		if err != nil {
			return 0, err
		}
		if err := Validate(refs, e.Updates); err != nil {
			return 0, err
		}

		e.Seq = head + 1
		won, err := s.TryAppend(e)
		if err != nil {
			return 0, err
		}
		if won {
			return e.Seq, nil
		}
		// Lost the race for this slot: loop to re-read the head and re-validate.
	}
}

// Validate checks each update's Old against the ref's current value in refs (a
// ref missing from the log counts as model.ZeroOID). Publish uses it before
// every append, and the coordinator uses it in PreReceive as an early check.
func Validate(refs map[string]string, updates []model.RefUpdate) error {
	for _, u := range updates {
		cur, ok := refs[u.Ref]
		if !ok {
			cur = model.ZeroOID
		}
		if cur != u.Old {
			return fmt.Errorf("%w: ref %s is %s in the log, but the push expects it to be %s",
				ErrConflict, u.Ref, describeOID(cur), describeOID(u.Old))
		}
	}
	return nil
}

// describeOID renders an object id for error messages: "absent" for the zero id, else its short form.
func describeOID(oid string) string {
	if oid == model.ZeroOID {
		return "absent"
	}
	if len(oid) > 7 {
		oid = oid[:7]
	}
	return "at " + oid
}
