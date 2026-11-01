package store_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"

	"toyengine/internal/model"
	"toyengine/internal/store"
)

func entry(seq uint64, id string) model.Entry {
	return model.Entry{
		Seq:     seq,
		PushID:  id,
		PackKey: "packs/x.pack",
		Updates: []model.RefUpdate{{Ref: "refs/heads/main", Old: model.ZeroOID, New: strings.Repeat("a", 40)}},
	}
}

// runContract is the behavioural spec of store.Store. MemStore passes it today;
// LocalDirStore must pass it once you implement it.
func runContract(t *testing.T, mk func(t *testing.T) store.Store) {
	t.Run("HeadEmpty", func(t *testing.T) {
		s := mk(t)
		if h, err := s.Head(); err != nil || h != 0 {
			t.Fatalf("Head = %d, %v; want 0, nil", h, err)
		}
	})

	t.Run("AppendRoundTrip", func(t *testing.T) {
		s := mk(t)
		e := entry(1, "p1")
		won, err := s.TryAppend(e)
		if err != nil || !won {
			t.Fatalf("TryAppend = %v, %v; want true, nil", won, err)
		}
		if h, _ := s.Head(); h != 1 {
			t.Fatalf("Head = %d; want 1", h)
		}
		got, err := s.ReadEntry(1)
		if err != nil || !reflect.DeepEqual(got, e) {
			t.Fatalf("ReadEntry = %+v, %v; want %+v", got, err, e)
		}
	})

	t.Run("FirstWriterWins", func(t *testing.T) {
		s := mk(t)
		first, second := entry(1, "first"), entry(1, "second")
		if won, err := s.TryAppend(first); err != nil || !won {
			t.Fatalf("first: %v, %v", won, err)
		}
		won, err := s.TryAppend(second)
		if err != nil || won {
			t.Fatalf("second: won=%v err=%v; want false, nil", won, err)
		}
		got, _ := s.ReadEntry(1)
		if got.PushID != "first" {
			t.Fatalf("slot was overwritten: %+v", got)
		}
	})

	t.Run("ReadMissingEntry", func(t *testing.T) {
		s := mk(t)
		if _, err := s.ReadEntry(1); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v; want fs.ErrNotExist", err)
		}
	})

	t.Run("BlobRoundTripAndIdempotent", func(t *testing.T) {
		s := mk(t)
		for i := 0; i < 2; i++ {
			if err := s.PutBlob("packs/abc.pack", strings.NewReader("hello pack")); err != nil {
				t.Fatalf("PutBlob #%d: %v", i, err)
			}
		}
		rc, err := s.GetBlob("packs/abc.pack")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		if b, _ := io.ReadAll(rc); !bytes.Equal(b, []byte("hello pack")) {
			t.Fatalf("got %q", b)
		}
	})

	t.Run("MissingBlob", func(t *testing.T) {
		s := mk(t)
		if _, err := s.GetBlob("packs/nope.pack"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v; want fs.ErrNotExist", err)
		}
	})

	t.Run("ConcurrentAppendIsDenseAndUnique", func(t *testing.T) {
		s := mk(t)
		const n = 50
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for {
					h, err := s.Head()
					if err != nil {
						t.Errorf("Head: %v", err)
						return
					}
					won, err := s.TryAppend(entry(h+1, fmt.Sprintf("push-%d", i)))
					if err != nil {
						t.Errorf("TryAppend(%d): %v", h+1, err)
						return
					}
					if won {
						return
					}
				}
			}(i)
		}
		wg.Wait()
		if h, _ := s.Head(); h != n {
			t.Fatalf("Head = %d; want %d", h, n)
		}
		seen := map[string]bool{}
		for seq := uint64(1); seq <= n; seq++ {
			e, err := s.ReadEntry(seq)
			if err != nil {
				t.Fatalf("ReadEntry(%d): %v", seq, err)
			}
			if e.Seq != seq {
				t.Fatalf("slot %d holds entry with Seq %d", seq, e.Seq)
			}
			if seen[e.PushID] {
				t.Fatalf("push %s appears twice", e.PushID)
			}
			seen[e.PushID] = true
		}
	})
}

func TestMemStoreContract(t *testing.T) {
	runContract(t, func(t *testing.T) store.Store { return store.NewMemStore() })
}

func TestLocalDirStoreContract(t *testing.T) {
	runContract(t, func(t *testing.T) store.Store {
		s, err := store.NewLocalDirStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}
