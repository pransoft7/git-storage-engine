package main

import (
	"os"
	"path/filepath"
	"testing"

	"toyengine/internal/model"
	"toyengine/internal/store"
)

// inspect must never create directories: whoever creates them owns them (found in the first
// integration run, where inspect run as root made root-owned store dirs that broke pushes).
func TestInspectDoesNotCreateDirectories(t *testing.T) {
	t.Setenv("TOYENGINE_DIR", t.TempDir())
	if rc := runInspect("ghost"); rc == 0 {
		t.Fatal("inspect of a repo with no log should fail")
	}
	entries, _ := os.ReadDir(dataDir())
	if len(entries) != 0 {
		t.Fatalf("inspect created %d entries under the data dir; want none", len(entries))
	}
}

func TestInspectReadsAnExistingLog(t *testing.T) {
	t.Setenv("TOYENGINE_DIR", t.TempDir())
	st, err := store.NewLocalDirStore(filepath.Join(dataDir(), "store", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	e := model.Entry{Seq: 1, PushID: "1", Updates: []model.RefUpdate{{Ref: "refs/heads/main", Old: model.ZeroOID, New: "a1b2c3d4"}}}
	if won, err := st.TryAppend(e); err != nil || !won {
		t.Fatal(won, err)
	}
	if rc := runInspect("demo"); rc != 0 {
		t.Fatalf("inspect of an existing log returned %d", rc)
	}
}
