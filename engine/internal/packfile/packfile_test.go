package packfile_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"toyengine/internal/packfile"
)

func writePack(t *testing.T, dir, sha string, objects uint32) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[8:], objects)
	if err := os.WriteFile(filepath.Join(dir, "pack", "pack-"+sha+".pack"), append(b, "x"...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	sha := strings.Repeat("ab", 20)

	t.Run("unset quarantine", func(t *testing.T) {
		info, err := packfile.Find("")
		if err != nil || info != nil {
			t.Fatalf("got %v, %v; want nil, nil", info, err)
		}
	})
	t.Run("no pack dir", func(t *testing.T) {
		info, err := packfile.Find(t.TempDir())
		if err != nil || info != nil {
			t.Fatalf("got %v, %v; want nil, nil", info, err)
		}
	})
	t.Run("one pack", func(t *testing.T) {
		q := t.TempDir()
		writePack(t, q, sha, 3)
		info, err := packfile.Find(q)
		if err != nil || info == nil {
			t.Fatalf("got %v, %v", info, err)
		}
		if info.SHA != sha || info.Objects != 3 {
			t.Fatalf("got %+v", info)
		}
	})
	t.Run("empty pack", func(t *testing.T) {
		q := t.TempDir()
		writePack(t, q, sha, 0)
		info, err := packfile.Find(q)
		if err != nil || info == nil || info.Objects != 0 {
			t.Fatalf("got %+v, %v", info, err)
		}
	})
	t.Run("two packs is an error", func(t *testing.T) {
		q := t.TempDir()
		writePack(t, q, sha, 1)
		writePack(t, q, strings.Repeat("cd", 20), 1)
		if _, err := packfile.Find(q); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("bad magic", func(t *testing.T) {
		q := t.TempDir()
		_ = os.MkdirAll(filepath.Join(q, "pack"), 0o755)
		_ = os.WriteFile(filepath.Join(q, "pack", "pack-"+sha+".pack"), []byte("NOTAPACKFILE"), 0o644)
		if _, err := packfile.Find(q); err == nil {
			t.Fatal("want error")
		}
	})
}
