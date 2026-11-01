// Package coordinator holds the rules that tie Git's hook sequence to the log. It
// decides WHEN to stage, publish, skip or reject; it knows nothing about processes,
// stdin or exit codes (cmd/toyengine translates those).
//
// Flow of a git push:
//
//	pre-receive -> [objects migrate] -> prepared -> [ref moves] -> committed -> client ack
//	PreReceive:     refuse or stage     Prepared:     publish      Committed:    bookkeeping
//	                                    to the log                 (Aborted: log only)
//
// Rules:
//   - All hooks of one push share a PushID (the receive-pack pid). The pending
//     record in node state is the mailbox from PreReceive to Prepared and Committed.
//   - Only pre-receive can refuse cleanly ("remote rejected"). An error in prepared
//     aborts the push and can hang the client, so refuse early and fail in prepared
//     only for real trouble.
//   - The log write (in Prepared) always happens before Git moves the ref, so a
//     push the user sees succeed is already in the log.
//   - Some transactions are not pushes (git update-ref): no pending record means
//     "not ours", and the hook returns nil.
//   - Odd push shapes: an empty pack (ref created at a known commit), no pack at all
//     (delete-only push), and an extra HEAD line on a first push.
//   - Git also fires extra events when it deletes a ref: an internal aborted before
//     the real prepared (loose ref), and prepared/committed twice (packed ref). So an
//     abort does not mean the push failed: Aborted leaves the pending record alone, and
//     Prepared ignores a push it has already published.
package coordinator

import (
	"errors"
	"fmt"
	"os"

	"toyengine/internal/model"
	"toyengine/internal/node"
	"toyengine/internal/packfile"
	"toyengine/internal/store"
	"toyengine/internal/wal"
)

// RejectError is a clean, user-facing refusal that cmd/toyengine shows to the
// pusher. Only PreReceive returns it.
type RejectError struct{ Msg string }

// Error returns the message shown to the pusher.
func (e *RejectError) Error() string { return e.Msg }

// Coordinator ties the store, node state and an optional logger together; cmd/toyengine
// builds it (tests build it with a MemStore).
type Coordinator struct {
	Store store.Store
	Node  *node.State
	Logf  func(format string, args ...any) // optional; use c.logf
}

// logf logs through Logf when one is set.
func (c *Coordinator) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// PreReceiveInput is everything the pre-receive hook knows about a push.
type PreReceiveInput struct {
	PushID         string            // receive-pack pid
	Updates        []model.RefUpdate // from stdin: one per ref being pushed
	QuarantinePath string            // $GIT_QUARANTINE_PATH; "" if unset
}

// PreReceive refuses or stages a push: it checks the push against the log, uploads
// the pack and saves the draft entry. Nothing is published to the log here.
//
// Known v1 limitation: the pack is uploaded and the draft staged before Git takes the ref
// lock. A push that then loses a same-ref race fails inside Git before any hook fires, so it
// leaves an orphan blob and a stale pending file that nothing cleans up (M2). On one node
// such races are decided by Git's ref lock, not by our CAS.
func (c *Coordinator) PreReceive(in PreReceiveInput) error {
	// v1: one log entry = one ref update. A non-atomic multi-ref push runs as several
	// separate ref transactions (one "prepared" per ref), which would not match a single
	// pending entry, and a hook cannot tell whether --atomic was used. So we reject
	// multi-ref pushes up front, where Git can report it cleanly.
	if len(in.Updates) != 1 {
		return &RejectError{Msg: "push exactly one ref at a time"}
	}

	head, err := c.Store.Head()
	if err != nil {
		return err
	}
	refs, err := wal.FoldRefs(c.Store, head)
	if err != nil {
		return err
	}
	if err := wal.Validate(refs, in.Updates); err != nil {
		if errors.Is(err, wal.ErrConflict) {
			return &RejectError{Msg: err.Error()}
		}
		return err
	}

	packKey, err := c.uploadPack(in.QuarantinePath)
	if err != nil {
		return err
	}

	return c.Node.SavePending(node.Pending{
		PushID: in.PushID,
		Entry:  model.Entry{PushID: in.PushID, Updates: in.Updates, PackKey: packKey},
	})
}

// uploadPack stores the quarantine pack in the blob store and returns its key, or ""
// when there is nothing to upload (no pack, or an empty one).
func (c *Coordinator) uploadPack(quarantine string) (string, error) {
	info, err := packfile.Find(quarantine)
	if err != nil {
		return "", err
	}
	if info == nil || info.Objects == 0 {
		return "", nil
	}

	f, err := os.Open(info.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	key := model.PackKey(info.SHA)
	if err := c.Store.PutBlob(key, f); err != nil {
		return "", fmt.Errorf("uploading pack %s: %w", info.SHA, err)
	}
	return key, nil
}

// Prepared publishes the staged entry to the log while Git holds the ref lock, then
// records its slot. No staged push means "not ours" (e.g. git update-ref): ignored.
// A repeat for a push already published (Git fires prepared twice on a packed delete)
// is ignored too.
func (c *Coordinator) Prepared(pushID string) error {
	pending, err := c.Node.LoadPending(pushID)
	if err != nil {
		return err
	}
	if pending == nil {
		c.logf("push %s: nothing staged, not a push of ours; ignoring", pushID)
		return nil
	}
	if pending.Entry.Seq != 0 {
		// Already published - Git can fire more than once per push
		// This is found to happen in delete packed ref
		c.logf("push %s: already published as seq %d; ignoring repeated prepared", pushID, pending.Entry.Seq)
		return nil
	}
	seq, err := wal.Publish(c.Store, pending.Entry)
	if err != nil {
		return err
	}

	// Best effort: the entry is already in the log, so failing to note its slot locally
	// must not abort the push.
	c.logf("push %s: published seq=%d %s", pushID, seq, describeUpdates(pending.Entry))
	pending.Entry.Seq = seq
	err = c.Node.SavePending(*pending)
	if err != nil {
		c.logf("saving pending after publish: %v", err)
	}
	return nil
}

// Committed runs after Git moved the ref: it records the entry's slot as applied and
// drops the pending record (none means "not ours"; on a packed delete it runs twice
// and the second call finds nothing).
func (c *Coordinator) Committed(pushID string) error {
	pending, err := c.Node.LoadPending(pushID)
	if err != nil {
		return err
	}
	if pending == nil {
		c.logf("push %s: nothing staged; nothing to record", pushID)
		return nil
	}
	if pending.Entry.Seq != 0 {
		err = c.Node.SetApplied(pending.Entry.Seq)
		if err != nil {
			return err
		}
	}
	if err := c.Node.DeletePending(pushID); err != nil {
		return err
	}
	c.logf("push %s: committed, applied=%d", pushID, pending.Entry.Seq)
	return nil
}

// describeUpdates renders an entry's ref changes for log lines, e.g. "refs/heads/main abc1234->def5678".
func describeUpdates(e model.Entry) string {
	out := ""
	for i, u := range e.Updates {
		if i > 0 {
			out += ", "
		}
		out += u.Ref + " " + short(u.Old) + "->" + short(u.New)
	}
	return out
}

func short(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}

// Aborted runs when a ref transaction is rolled back. It deliberately leaves the pending
// record alone: Git also aborts internal helper transactions (e.g. the packed-refs one on
// a delete) before the real one, so an abort does not mean the push failed. Stale pending
// files are an M2 cleanup. A published entry is never retracted (the log is append-only).
func (c *Coordinator) Aborted(pushID string) error {
	c.logf("aborted event for push %s", pushID)
	return nil
}
