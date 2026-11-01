package coordinator_test

import (
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"toyengine/internal/coordinator"
	"toyengine/internal/model"
	"toyengine/internal/node"
	"toyengine/internal/store"
	"toyengine/internal/wal"
)

const (
	main    = "refs/heads/main"
	pushID  = "4242"
	packSHA = "abababababababababababababababababababab"
)

func oid(c string) string { return strings.Repeat(c, 40) }

type env struct {
	c  *coordinator.Coordinator
	st *store.MemStore
	ns *node.State
}

func newEnv(t *testing.T) env {
	t.Helper()
	st := store.NewMemStore()
	ns, err := node.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return env{c: &coordinator.Coordinator{Store: st, Node: ns, Logf: t.Logf}, st: st, ns: ns}
}

// quarantine builds a fake $GIT_QUARANTINE_PATH containing one pack.
func quarantine(t *testing.T, objects uint32) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[8:], objects)
	b = append(b, "pack-payload"...)
	if err := os.WriteFile(filepath.Join(dir, "pack", "pack-"+packSHA+".pack"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func create(ref, new string) model.RefUpdate {
	return model.RefUpdate{Ref: ref, Old: model.ZeroOID, New: new}
}

func blob(t *testing.T, s store.Store, key string) ([]byte, error) {
	t.Helper()
	rc, err := s.GetBlob(key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// ---- PreReceive ------------------------------------------------------------

func TestPreReceiveRejectsMultiRefPush(t *testing.T) {
	e := newEnv(t)
	err := e.c.PreReceive(coordinator.PreReceiveInput{
		PushID:  pushID,
		Updates: []model.RefUpdate{create(main, oid("a")), create("refs/heads/dev", oid("a"))},
	})
	var rej *coordinator.RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v; want *RejectError", err)
	}
	if p, _ := e.ns.LoadPending(pushID); p != nil {
		t.Fatal("a rejected push must not leave a pending record")
	}
}

func TestPreReceiveRejectsNoUpdates(t *testing.T) {
	e := newEnv(t)
	err := e.c.PreReceive(coordinator.PreReceiveInput{PushID: pushID})
	var rej *coordinator.RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v; want *RejectError", err)
	}
}

func TestPreReceiveUploadsPackAndStagesPending(t *testing.T) {
	e := newEnv(t)
	q := quarantine(t, 3)
	ups := []model.RefUpdate{create(main, oid("a"))}
	if err := e.c.PreReceive(coordinator.PreReceiveInput{PushID: pushID, Updates: ups, QuarantinePath: q}); err != nil {
		t.Fatal(err)
	}

	want, _ := os.ReadFile(filepath.Join(q, "pack", "pack-"+packSHA+".pack"))
	got, err := blob(t, e.st, model.PackKey(packSHA))
	if err != nil || string(got) != string(want) {
		t.Fatalf("stored pack = %q, %v; want the quarantine pack bytes", got, err)
	}
	p, err := e.ns.LoadPending(pushID)
	if err != nil || p == nil {
		t.Fatalf("pending = %v, %v", p, err)
	}
	if p.Entry.PackKey != model.PackKey(packSHA) || !reflect.DeepEqual(p.Entry.Updates, ups) {
		t.Fatalf("pending entry = %+v", p.Entry)
	}
	if p.Entry.Seq != 0 {
		t.Fatal("PreReceive must not publish")
	}
	if h, _ := e.st.Head(); h != 0 {
		t.Fatalf("log has %d entries after pre-receive; want 0", h)
	}
}

func TestPreReceiveEmptyPackIsNotUploaded(t *testing.T) {
	e := newEnv(t)
	q := quarantine(t, 0)
	if err := e.c.PreReceive(coordinator.PreReceiveInput{PushID: pushID, Updates: []model.RefUpdate{create("refs/heads/x", oid("a"))}, QuarantinePath: q}); err != nil {
		t.Fatal(err)
	}
	if _, err := blob(t, e.st, model.PackKey(packSHA)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty pack was uploaded (err=%v)", err)
	}
	p, _ := e.ns.LoadPending(pushID)
	if p == nil || p.Entry.PackKey != "" {
		t.Fatalf("pending = %+v; want PackKey \"\"", p)
	}
}

func TestPreReceiveWithoutQuarantine(t *testing.T) { // e.g. a delete-only push
	e := newEnv(t)
	// the ref being deleted must exist in the log, or the early conflict check rejects the push
	if _, err := wal.Publish(e.st, model.Entry{PushID: "setup", Updates: []model.RefUpdate{create("refs/heads/old", oid("a"))}}); err != nil {
		t.Fatal(err)
	}
	del := model.RefUpdate{Ref: "refs/heads/old", Old: oid("a"), New: model.ZeroOID}
	if err := e.c.PreReceive(coordinator.PreReceiveInput{PushID: pushID, Updates: []model.RefUpdate{del}}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.ns.LoadPending(pushID)
	if p == nil || p.Entry.PackKey != "" {
		t.Fatalf("pending = %+v", p)
	}
}

// The log and the repo have diverged (here: the log already has main, the push
// claims to create it). Must be a clean rejection, before anything is uploaded.
func TestPreReceiveRejectsPushThatConflictsWithLog(t *testing.T) {
	e := newEnv(t)
	if _, err := wal.Publish(e.st, model.Entry{PushID: "setup", Updates: []model.RefUpdate{create(main, oid("f"))}}); err != nil {
		t.Fatal(err)
	}
	err := e.c.PreReceive(coordinator.PreReceiveInput{
		PushID: pushID, Updates: []model.RefUpdate{create(main, oid("a"))}, QuarantinePath: quarantine(t, 3),
	})
	var rej *coordinator.RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v; want *RejectError", err)
	}
	if _, err := blob(t, e.st, model.PackKey(packSHA)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("pack was uploaded despite the rejection (err=%v)", err)
	}
	if p, _ := e.ns.LoadPending(pushID); p != nil {
		t.Fatal("a rejected push must not leave a pending record")
	}
}

// ---- Prepared --------------------------------------------------------------

func stage(t *testing.T, e env, objects uint32) {
	t.Helper()
	err := e.c.PreReceive(coordinator.PreReceiveInput{
		PushID: pushID, Updates: []model.RefUpdate{create(main, oid("a"))}, QuarantinePath: quarantine(t, objects),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWithoutPendingIsNoop(t *testing.T) { // e.g. `git update-ref`
	e := newEnv(t)
	if err := e.c.Prepared("999"); err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if h, _ := e.st.Head(); h != 0 {
		t.Fatalf("log has %d entries; a non-push must not be logged", h)
	}
}

func TestPreparedPublishesStagedEntry(t *testing.T) {
	e := newEnv(t)
	stage(t, e, 3)
	if err := e.c.Prepared(pushID); err != nil {
		t.Fatal(err)
	}
	if h, _ := e.st.Head(); h != 1 {
		t.Fatalf("Head = %d; want 1", h)
	}
	got, _ := e.st.ReadEntry(1)
	if got.Seq != 1 || got.PackKey != model.PackKey(packSHA) {
		t.Fatalf("entry = %+v", got)
	}
	if !reflect.DeepEqual(got.Updates, []model.RefUpdate{create(main, oid("a"))}) {
		t.Fatalf("entry updates = %+v; want exactly what was staged", got.Updates)
	}
	p, _ := e.ns.LoadPending(pushID)
	if p == nil || p.Entry.Seq != 1 {
		t.Fatalf("pending after publish = %+v", p)
	}
}

func TestPreparedReturnsErrorOnConflictAndDoesNotAppend(t *testing.T) {
	e := newEnv(t)
	stage(t, e, 3) // staged: create main -> a
	// Someone else created main in the meantime.
	if _, err := wal.Publish(e.st, model.Entry{PushID: "other", Updates: []model.RefUpdate{create(main, oid("f"))}}); err != nil {
		t.Fatal(err)
	}
	err := e.c.Prepared(pushID)
	if !errors.Is(err, wal.ErrConflict) {
		t.Fatalf("err = %v; want wal.ErrConflict", err)
	}
	if h, _ := e.st.Head(); h != 1 {
		t.Fatalf("Head = %d; want 1 (only the other writer's entry)", h)
	}
}

// ---- Committed / Aborted ---------------------------------------------------

func TestCommittedRecordsAppliedAndClearsPending(t *testing.T) {
	e := newEnv(t)
	stage(t, e, 3)
	if err := e.c.Prepared(pushID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Committed(pushID); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.ns.Applied(); n != 1 {
		t.Fatalf("Applied = %d; want 1", n)
	}
	if p, _ := e.ns.LoadPending(pushID); p != nil {
		t.Fatal("pending record should be gone")
	}
}

func TestCommittedWithoutPendingIsNoop(t *testing.T) {
	e := newEnv(t)
	if err := e.c.Committed("999"); err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if n, _ := e.ns.Applied(); n != 0 {
		t.Fatalf("Applied = %d; want 0", n)
	}
}

// An abort must NOT discard the staged push: Git also aborts internal helper
// transactions (e.g. the packed-refs one on a delete) before the real one is prepared.
func TestAbortedKeepsPendingRecord(t *testing.T) {
	e := newEnv(t)
	stage(t, e, 3)
	for i := 0; i < 2; i++ { // repeated aborts are harmless
		if err := e.c.Aborted(pushID); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := e.ns.LoadPending(pushID); p == nil {
		t.Fatal("Aborted must leave the pending record in place")
	}
}

// Found in the first integration run: for a delete-only push Git fires an internal,
// throwaway transaction that is ABORTED before the real one is prepared:
//
//	pre-receive -> aborted (zero -> zero) -> prepared -> committed
//
// An abort must not discard the staged push, or the delete never reaches the log.
func TestDeletePushSurvivesInternalAbort(t *testing.T) {
	e := newEnv(t)
	if _, err := wal.Publish(e.st, model.Entry{PushID: "setup", Updates: []model.RefUpdate{create("refs/heads/old", oid("a"))}}); err != nil {
		t.Fatal(err)
	}
	del := model.RefUpdate{Ref: "refs/heads/old", Old: oid("a"), New: model.ZeroOID}
	if err := e.c.PreReceive(coordinator.PreReceiveInput{PushID: pushID, Updates: []model.RefUpdate{del}}); err != nil {
		t.Fatal(err)
	}

	if err := e.c.Aborted(pushID); err != nil { // the internal throwaway transaction
		t.Fatal(err)
	}
	if err := e.c.Prepared(pushID); err != nil { // the real delete
		t.Fatal(err)
	}

	if h, _ := e.st.Head(); h != 2 {
		t.Fatalf("log has %d entries; want 2: the delete was never published", h)
	}
}

// Found in the first integration run: deleting a ref that is stored in packed-refs makes Git
// fire prepared TWICE in one push (an internal packed-refs transaction, then the real one).
// The second call must be a no-op; publishing again conflicts and aborts the push.
func TestPreparedTwiceDoesNotPublishTwice(t *testing.T) {
	e := newEnv(t)
	stage(t, e, 3)
	if err := e.c.Prepared(pushID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Prepared(pushID); err != nil {
		t.Fatalf("second prepared must be a no-op, got %v", err)
	}
	if h, _ := e.st.Head(); h != 1 {
		t.Fatalf("log has %d entries; want exactly 1", h)
	}
	if err := e.c.Committed(pushID); err != nil { // and committed may also come twice
		t.Fatal(err)
	}
	if err := e.c.Committed(pushID); err != nil {
		t.Fatalf("second committed must be a no-op, got %v", err)
	}
}
