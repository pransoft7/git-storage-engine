// Package model holds the types shared by every layer of the engine.
package model

// ZeroOID is Git's "no object": as an Old value it means "the ref must not
// exist yet" (create); as a New value it means "delete the ref".
const ZeroOID = "0000000000000000000000000000000000000000"

// RefUpdate is one line of a push: move Ref from Old to New.
type RefUpdate struct {
	Ref string `json:"ref"`
	Old string `json:"old"`
	New string `json:"new"`
}

// Entry is one record in the write-ahead log: a single published push.
// Entries are immutable once written.
type Entry struct {
	Seq     uint64      `json:"seq"`                // position in the log, starting at 1
	PushID  string      `json:"push_id"`            // identifies the push that produced it
	Updates []RefUpdate `json:"updates"`            // ref changes this push made
	PackKey string      `json:"pack_key,omitempty"` // blob key of the pack; "" if the push carried no objects
}

// IsOID reports whether s looks like a full 40-hex SHA-1 object id.
func IsOID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// PackKey is the blob key under which a pack with the given checksum is stored.
func PackKey(packSHA string) string { return "packs/" + packSHA + ".pack" }
