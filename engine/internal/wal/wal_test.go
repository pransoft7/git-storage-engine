package wal_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"toyengine/internal/model"
	"toyengine/internal/store"
	"toyengine/internal/wal"
)

func oid(c string) string { return strings.Repeat(c, 40) }

func push(id string, updates ...model.RefUpdate) model.Entry {
	return model.Entry{PushID: id, Updates: updates}
}

func upd(ref, old, new string) model.RefUpdate { return model.RefUpdate{Ref: ref, Old: old, New: new} }

const main = "refs/heads/main"

func TestFoldRefs(t *testing.T) {
	s := store.NewMemStore()
	z := model.ZeroOID
	steps := []model.Entry{
		push("1", upd(main, z, oid("a")), upd("refs/heads/dev", z, oid("b"))),
		push("2", upd(main, oid("a"), oid("c"))),
		push("3", upd("refs/heads/dev", oid("b"), z)), // delete
	}
	for i, e := range steps {
		e.Seq = uint64(i + 1)
		if won, err := s.TryAppend(e); err != nil || !won {
			t.Fatal(won, err)
		}
	}
	for upTo, want := range map[uint64]map[string]string{
		0: {},
		1: {main: oid("a"), "refs/heads/dev": oid("b")},
		2: {main: oid("c"), "refs/heads/dev": oid("b")},
		3: {main: oid("c")},
	} {
		got, err := wal.FoldRefs(s, upTo)
		if err != nil {
			t.Fatalf("FoldRefs(%d): %v", upTo, err)
		}
		if len(got) != len(want) {
			t.Fatalf("FoldRefs(%d) = %v; want %v", upTo, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("FoldRefs(%d)[%s] = %q; want %q", upTo, k, got[k], v)
			}
		}
	}
}

func TestPublishAssignsConsecutiveSeqs(t *testing.T) {
	s := store.NewMemStore()
	z := model.ZeroOID
	seq, err := wal.Publish(s, push("p1", upd(main, z, oid("a"))))
	if err != nil || seq != 1 {
		t.Fatalf("got %d, %v; want 1", seq, err)
	}
	seq, err = wal.Publish(s, push("p2", upd(main, oid("a"), oid("b"))))
	if err != nil || seq != 2 {
		t.Fatalf("got %d, %v; want 2", seq, err)
	}
	e, _ := s.ReadEntry(2)
	if e.Seq != 2 || e.PushID != "p2" {
		t.Fatalf("stored entry = %+v", e)
	}
}

func TestPublishIgnoresCallerSeq(t *testing.T) {
	s := store.NewMemStore()
	e := push("p", upd(main, model.ZeroOID, oid("a")))
	e.Seq = 99
	if seq, err := wal.Publish(s, e); err != nil || seq != 1 {
		t.Fatalf("got %d, %v; want 1", seq, err)
	}
}

func TestPublishConflicts(t *testing.T) {
	z := model.ZeroOID
	setup := func() *store.MemStore {
		s := store.NewMemStore()
		if _, err := wal.Publish(s, push("base", upd(main, z, oid("a")))); err != nil {
			t.Fatal(err)
		}
		return s
	}
	cases := map[string]model.Entry{
		"stale old value":          push("x", upd(main, oid("b"), oid("c"))),
		"create over existing ref": push("x", upd(main, z, oid("c"))),
		"update of missing ref":    push("x", upd("refs/heads/nope", oid("a"), oid("c"))),
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			s := setup()
			seq, err := wal.Publish(s, e)
			if !errors.Is(err, wal.ErrConflict) {
				t.Fatalf("got seq=%d err=%v; want ErrConflict", seq, err)
			}
			if h, _ := s.Head(); h != 1 {
				t.Fatalf("log grew to %d on a conflict", h)
			}
		})
	}
}

// racyStore makes the first TryAppend lose: just before the caller's attempt, a
// competing entry takes the slot, exactly as a concurrent push on another node
// would. The underlying store then rejects the caller's attempt as "taken".
type racyStore struct {
	store.Store
	rival model.Entry
	armed bool
}

func newRacy(rival model.Entry) *racyStore {
	return &racyStore{Store: store.NewMemStore(), rival: rival, armed: true}
}

func (r *racyStore) TryAppend(e model.Entry) (bool, error) {
	if r.armed {
		r.armed = false
		rv := r.rival
		rv.Seq = e.Seq
		if won, err := r.Store.TryAppend(rv); err != nil || !won {
			panic(fmt.Sprintf("test setup: rival append failed: %v %v", won, err))
		}
	}
	return r.Store.TryAppend(e)
}

func TestPublishRetriesAfterLosingRace(t *testing.T) {
	z := model.ZeroOID
	r := newRacy(push("rival", upd("refs/heads/other", z, oid("d"))))
	seq, err := wal.Publish(r, push("mine", upd(main, z, oid("a"))))
	if err != nil || seq != 2 {
		t.Fatalf("got %d, %v; want seq 2 after losing slot 1", seq, err)
	}
	e1, _ := r.ReadEntry(1)
	e2, _ := r.ReadEntry(2)
	if e1.PushID != "rival" || e2.PushID != "mine" {
		t.Fatalf("log = %+v, %+v", e1, e2)
	}
}

// Losing a race to a push that touches the SAME ref must surface as a conflict
// on the retry, not blindly append on top of it.
func TestPublishRevalidatesAfterLosingRace(t *testing.T) {
	z := model.ZeroOID
	r := newRacy(push("rival", upd(main, z, oid("d"))))
	_, err := wal.Publish(r, push("mine", upd(main, z, oid("a"))))
	if !errors.Is(err, wal.ErrConflict) {
		t.Fatalf("err = %v; want ErrConflict", err)
	}
	if h, _ := r.Head(); h != 1 {
		t.Fatalf("log has %d entries; want only the rival's", h)
	}
}

func TestPublishConcurrentDifferentRefs(t *testing.T) {
	s := store.NewMemStore()
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := push(fmt.Sprint(i), upd(fmt.Sprintf("refs/heads/b%d", i), model.ZeroOID, oid("a")))
			if _, err := wal.Publish(s, e); err != nil {
				t.Errorf("Publish %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if h, _ := s.Head(); h != n {
		t.Fatalf("Head = %d; want %d", h, n)
	}
	refs, err := wal.FoldRefs(s, n)
	if err != nil || len(refs) != n {
		t.Fatalf("FoldRefs = %d refs, %v; want %d", len(refs), err, n)
	}
}

// The engine's central guarantee, on the real disk store: many independent
// "nodes" (separate LocalDirStore instances on one directory) publish at once.
func TestPublishRacingNodesOnDisk(t *testing.T) {
	root := t.TempDir()

	t.Run("different refs: everyone gets a slot, log is dense", func(t *testing.T) {
		const n = 20
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				s, _ := store.NewLocalDirStore(root)
				e := push(fmt.Sprint(i), upd(fmt.Sprintf("refs/heads/n%d", i), model.ZeroOID, oid("a")))
				if _, err := wal.Publish(s, e); err != nil {
					t.Errorf("node %d: %v", i, err)
				}
			}(i)
		}
		wg.Wait()
		s, _ := store.NewLocalDirStore(root)
		if h, _ := s.Head(); h != n {
			t.Fatalf("Head = %d; want %d", h, n)
		}
		if refs, err := wal.FoldRefs(s, n); err != nil || len(refs) != n {
			t.Fatalf("FoldRefs = %d refs, %v; want %d", len(refs), err, n)
		}
	})

	t.Run("same ref: exactly one winner, the rest see a conflict", func(t *testing.T) {
		const n = 10
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins, conflicts := 0, 0
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				s, _ := store.NewLocalDirStore(root)
				e := push(fmt.Sprint(i), upd("refs/heads/contended", model.ZeroOID, oid(string(rune('a'+i)))))
				_, err := wal.Publish(s, e)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
				case errors.Is(err, wal.ErrConflict):
					conflicts++
				default:
					t.Errorf("node %d: unexpected error %v", i, err)
				}
			}(i)
		}
		wg.Wait()
		if wins != 1 || conflicts != n-1 {
			t.Fatalf("wins=%d conflicts=%d; want 1 and %d", wins, conflicts, n-1)
		}
	})
}

// The conflict message is shown to the pusher: it must read well (no all-zero ids) and keep ErrConflict.
func TestConflictMessageIsReadable(t *testing.T) {
	s := store.NewMemStore()
	z := model.ZeroOID
	if _, err := wal.Publish(s, push("base", upd(main, z, oid("a")))); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		update model.RefUpdate
		want   []string
	}{
		"stale old value":     {upd(main, oid("b"), oid("c")), []string{"refs/heads/main", "at aaaaaaa", "at bbbbbbb"}},
		"create existing ref": {upd(main, z, oid("c")), []string{"refs/heads/main", "at aaaaaaa", "absent"}},
		"update missing ref":  {upd("refs/heads/nope", oid("a"), oid("c")), []string{"refs/heads/nope", "absent", "at aaaaaaa"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := wal.Publish(s, push("x", c.update))
			if !errors.Is(err, wal.ErrConflict) {
				t.Fatalf("err = %v; want ErrConflict", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("message %q lacks %q", err.Error(), w)
				}
			}
			if strings.Contains(err.Error(), "0000000") {
				t.Errorf("message %q leaks a zero id", err.Error())
			}
		})
	}
}
