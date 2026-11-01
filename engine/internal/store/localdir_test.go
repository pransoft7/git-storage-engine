package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"toyengine/internal/store"
)

// Tests about the on-disk behaviour that the generic contract can't see.

func TestLocalDirHeadIgnoresJunk(t *testing.T) {
	root := t.TempDir()
	s, err := store.NewLocalDirStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".tmp-123", "notes.txt", "1.json", "0000000x.json", "00000000.json"} {
		if err := os.WriteFile(filepath.Join(root, "log", name), []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if h, err := s.Head(); err != nil || h != 0 {
		t.Fatalf("Head = %d, %v; want 0", h, err)
	}
}

func TestLocalDirPersistsAcrossInstances(t *testing.T) {
	root := t.TempDir()
	a, _ := store.NewLocalDirStore(root)
	if won, err := a.TryAppend(entry(1, "p")); err != nil || !won {
		t.Fatal(won, err)
	}
	b, _ := store.NewLocalDirStore(root)
	if h, _ := b.Head(); h != 1 {
		t.Fatalf("second instance sees Head = %d; want 1", h)
	}
}

// Two independent store instances on the same directory model two nodes
// sharing one bucket: they must still agree on exactly one winner per slot.
func TestLocalDirTwoInstancesRace(t *testing.T) {
	root := t.TempDir()
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, _ := store.NewLocalDirStore(root) // each goroutine = its own "node"
			for {
				h, _ := s.Head()
				won, err := s.TryAppend(entry(h+1, fmt.Sprint(i)))
				if err != nil {
					t.Errorf("%v", err)
					return
				}
				if won {
					return
				}
			}
		}(i)
	}
	wg.Wait()
	s, _ := store.NewLocalDirStore(root)
	if h, _ := s.Head(); h != n {
		t.Fatalf("Head = %d; want %d", h, n)
	}
}

// Losing attempts must not leave temp files or partial slots behind.
func TestLocalDirNoDebrisAfterRaces(t *testing.T) {
	root := t.TempDir()
	s, _ := store.NewLocalDirStore(root)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				h, _ := s.Head()
				if won, err := s.TryAppend(entry(h+1, fmt.Sprint(i))); err != nil || won {
					return
				}
			}
		}(i)
	}
	wg.Wait()
	ents, err := os.ReadDir(filepath.Join(root, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != n {
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("log dir has %d files, want exactly %d slots: %v", len(ents), n, names)
	}
}

// ---- Step 1 tests: the READ side only. They place files by hand, so they do
// not depend on PutBlob/TryAppend being written yet. Run with:
//   go test ./internal/store -run LocalDirRead -v

func TestLocalDirReadBlob(t *testing.T) {
	root := t.TempDir()
	s, _ := store.NewLocalDirStore(root)
	if err := os.WriteFile(filepath.Join(root, "packs", "abc.pack"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := s.GetBlob("packs/abc.pack")
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
}

func TestLocalDirReadBlobMissing(t *testing.T) {
	s, _ := store.NewLocalDirStore(t.TempDir())
	if _, err := s.GetBlob("packs/nope.pack"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v; want fs.ErrNotExist", err)
	}
}

func TestLocalDirReadEntry(t *testing.T) {
	root := t.TempDir()
	s, _ := store.NewLocalDirStore(root)
	want := entry(1, "hand-written")
	b, _ := json.Marshal(want)
	if err := os.WriteFile(filepath.Join(root, "log", store.SlotName(1)), b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadEntry(1)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestLocalDirReadEntryMissing(t *testing.T) {
	s, _ := store.NewLocalDirStore(t.TempDir())
	if _, err := s.ReadEntry(1); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v; want fs.ErrNotExist", err)
	}
}

func TestLocalDirReadEntryCorrupt(t *testing.T) {
	root := t.TempDir()
	s, _ := store.NewLocalDirStore(root)
	if err := os.WriteFile(filepath.Join(root, "log", store.SlotName(1)), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadEntry(1); err == nil {
		t.Fatal("want an error for a corrupt entry, got nil")
	}
}

// ---- PutBlob housekeeping --------------------------------------------------

func TestLocalDirPutBlobFailureLeavesNothing(t *testing.T) {
	root := t.TempDir()
	s, _ := store.NewLocalDirStore(root)
	err := s.PutBlob("packs/x.pack", iotest.ErrReader(errors.New("boom")))
	if err == nil {
		t.Fatal("want an error from the failing reader")
	}
	ents, _ := os.ReadDir(filepath.Join(root, "packs"))
	if len(ents) != 0 {
		t.Fatalf("packs/ should be empty after a failed PutBlob, has %d entries", len(ents))
	}
}

func TestLocalDirPutBlobLeavesOnlyTheBlob(t *testing.T) {
	root := t.TempDir()
	s, _ := store.NewLocalDirStore(root)
	for i := 0; i < 2; i++ { // twice: idempotent, and still no temp files
		if err := s.PutBlob("packs/abc.pack", strings.NewReader("hello")); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(root, "packs"))
	if len(ents) != 1 || ents[0].Name() != "abc.pack" {
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("packs/ = %v; want exactly [abc.pack]", names)
	}
}
