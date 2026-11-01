package replay_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"toyengine/internal/model"
	"toyengine/internal/node"
	"toyengine/internal/replay"
	"toyengine/internal/store"
)

// git runs a git command, returns trimmed stdout, and fatals on error.
func git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// bareRepo returns the path to a new empty bare git repo.
func bareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, "init", "-q", "--bare", dir)
	return dir
}

// commitPack creates a working repo with one empty commit, packs it by cloning bare,
// stores the pack in ms under its content key, and returns (packKey, commitSHA, refName).
func commitPack(t *testing.T, ms *store.MemStore, msg string) (packKey, commitSHA, refName string) {
	t.Helper()
	work := t.TempDir()
	git(t, "init", "-q", work)
	git(t, "-C", work, "config", "user.email", "t@t.com")
	git(t, "-C", work, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", work, "add", "README")
	git(t, "-C", work, "commit", "-m", msg)
	commitSHA = git(t, "-C", work, "rev-parse", "HEAD")
	refName = "refs/heads/" + git(t, "-C", work, "rev-parse", "--abbrev-ref", "HEAD")

	bare := t.TempDir()
	git(t, "clone", "--no-local", "--bare", "-q", work, bare)

	packs, err := filepath.Glob(filepath.Join(bare, "objects", "pack", "*.pack"))
	if err != nil || len(packs) == 0 {
		t.Fatal("no pack in bare clone")
	}
	data, err := os.ReadFile(packs[0])
	if err != nil {
		t.Fatal(err)
	}
	// The filename is pack-<sha>.pack; the sha is the content key.
	base := filepath.Base(packs[0])
	sha := strings.TrimSuffix(strings.TrimPrefix(base, "pack-"), ".pack")
	packKey = model.PackKey(sha)
	if err := ms.PutBlob(packKey, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	return packKey, commitSHA, refName
}

// refValue returns the current value of ref in a bare repo, or "" if absent.
func refValue(t *testing.T, repoDir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", repoDir, "rev-parse", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mustAppend adds an entry to ms; fatals if the slot is already taken.
func mustAppend(t *testing.T, ms *store.MemStore, e model.Entry) {
	t.Helper()
	ok, err := ms.TryAppend(e)
	if err != nil || !ok {
		t.Fatalf("TryAppend seq=%d: ok=%v err=%v", e.Seq, ok, err)
	}
}

// openNodeState opens (or creates) the replay node state inside the target repo.
func openNodeState(t *testing.T, repoDir string) *node.State {
	t.Helper()
	ns, err := node.Open(filepath.Join(repoDir, "toyengine"))
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// TestApplyEmptyLog: no log entries → Apply returns nil, Applied stays 0.
func TestApplyEmptyLog(t *testing.T) {
	ms := store.NewMemStore()
	target := bareRepo(t)
	ns := openNodeState(t, target)

	if err := replay.Apply(ms, target, ns, replay.Opts{}); err != nil {
		t.Fatal(err)
	}
	if w, _ := ns.Applied(); w != 0 {
		t.Fatalf("want Applied=0, got %d", w)
	}
}

// TestApplyWithPack: one entry carrying a pack and a ref creation.
func TestApplyWithPack(t *testing.T) {
	ms := store.NewMemStore()
	packKey, commitSHA, refName := commitPack(t, ms, "init")
	mustAppend(t, ms, model.Entry{
		Seq: 1, PushID: "1", PackKey: packKey,
		Updates: []model.RefUpdate{{Ref: refName, Old: model.ZeroOID, New: commitSHA}},
	})

	target := bareRepo(t)
	ns := openNodeState(t, target)

	if err := replay.Apply(ms, target, ns, replay.Opts{}); err != nil {
		t.Fatal(err)
	}
	if got := refValue(t, target, refName); got != commitSHA {
		t.Fatalf("%s: want %s, got %q", refName, commitSHA, got)
	}
	if w, _ := ns.Applied(); w != 1 {
		t.Fatalf("want Applied=1, got %d", w)
	}
}

// TestApplyDelete: create a ref then delete it; the ref must be absent afterward.
func TestApplyDelete(t *testing.T) {
	ms := store.NewMemStore()
	packKey, commitSHA, refName := commitPack(t, ms, "init")
	mustAppend(t, ms, model.Entry{
		Seq: 1, PushID: "1", PackKey: packKey,
		Updates: []model.RefUpdate{{Ref: refName, Old: model.ZeroOID, New: commitSHA}},
	})
	mustAppend(t, ms, model.Entry{
		Seq: 2, PushID: "2",
		Updates: []model.RefUpdate{{Ref: refName, Old: commitSHA, New: model.ZeroOID}},
	})

	target := bareRepo(t)
	ns := openNodeState(t, target)

	if err := replay.Apply(ms, target, ns, replay.Opts{}); err != nil {
		t.Fatal(err)
	}
	if got := refValue(t, target, refName); got != "" {
		t.Fatalf("ref should be gone, got %s", got)
	}
	if w, _ := ns.Applied(); w != 2 {
		t.Fatalf("want Applied=2, got %d", w)
	}
}

// TestApplyIdempotent: running Apply twice produces the same result without error.
func TestApplyIdempotent(t *testing.T) {
	ms := store.NewMemStore()
	packKey, commitSHA, refName := commitPack(t, ms, "init")
	mustAppend(t, ms, model.Entry{
		Seq: 1, PushID: "1", PackKey: packKey,
		Updates: []model.RefUpdate{{Ref: refName, Old: model.ZeroOID, New: commitSHA}},
	})

	target := bareRepo(t)
	ns := openNodeState(t, target)

	if err := replay.Apply(ms, target, ns, replay.Opts{}); err != nil {
		t.Fatal(err)
	}
	if err := replay.Apply(ms, target, ns, replay.Opts{}); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if got := refValue(t, target, refName); got != commitSHA {
		t.Fatalf("%s: want %s, got %q", refName, commitSHA, got)
	}
	if w, _ := ns.Applied(); w != 1 {
		t.Fatalf("want Applied=1, got %d", w)
	}
}

// TestApplyResumes: entry 1 is already applied (watermark=1); Apply must process
// only entry 2, leaving entry 1's ref unchanged and entry 2's ref present.
func TestApplyResumes(t *testing.T) {
	ms := store.NewMemStore()
	// Two independent commits, stored under different ref names.
	packKey1, sha1, _ := commitPack(t, ms, "first")
	packKey2, sha2, _ := commitPack(t, ms, "second")
	mustAppend(t, ms, model.Entry{
		Seq: 1, PushID: "1", PackKey: packKey1,
		Updates: []model.RefUpdate{{Ref: "refs/heads/alpha", Old: model.ZeroOID, New: sha1}},
	})
	mustAppend(t, ms, model.Entry{
		Seq: 2, PushID: "2", PackKey: packKey2,
		Updates: []model.RefUpdate{{Ref: "refs/heads/beta", Old: model.ZeroOID, New: sha2}},
	})

	target := bareRepo(t)
	ns := openNodeState(t, target)

	// Simulate entry 1 already applied: import its pack and move the ref manually.
	r, err := ms.GetBlob(packKey1)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "--git-dir", target, "index-pack", "--stdin")
	cmd.Stdin = r
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("index-pack: %v\n%s", err, out)
	}
	r.Close()
	git(t, "--git-dir", target, "update-ref", "refs/heads/alpha", sha1)
	if err := ns.SetApplied(1); err != nil {
		t.Fatal(err)
	}

	// Apply: only entry 2 should be processed.
	if err := replay.Apply(ms, target, ns, replay.Opts{}); err != nil {
		t.Fatal(err)
	}
	if got := refValue(t, target, "refs/heads/alpha"); got != sha1 {
		t.Fatalf("refs/heads/alpha: want %s, got %q (should be unchanged)", sha1, got)
	}
	if got := refValue(t, target, "refs/heads/beta"); got != sha2 {
		t.Fatalf("refs/heads/beta: want %s, got %q", sha2, got)
	}
	if w, _ := ns.Applied(); w != 2 {
		t.Fatalf("want Applied=2, got %d", w)
	}
}
