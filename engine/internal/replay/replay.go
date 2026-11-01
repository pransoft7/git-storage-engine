// Package replay rebuilds or catches up a bare git repo by replaying the engine's
// write-ahead log. It is the Go equivalent of engine/scripts/rebuild-check.py.
package replay

import (
	"fmt"
	"os"
	"os/exec"
	"toyengine/internal/model"
	"toyengine/internal/node"
	"toyengine/internal/store"
)

// Opts controls replay behaviour.
type Opts struct {
	Logf func(string, ...any) // progress logger; nil = silent
}

// Apply replays the log in s into the bare git repo at repoDir, starting from
// the contiguous applied watermark in ns. It is resumable (safe to interrupt
// and re-run) and idempotent (running twice changes nothing).
//
// All git commands run with TOY_REPLAY=1 so any engine hooks installed in
// repoDir are no-ops and do not re-publish the ref updates to the log.
func Apply(s store.Store, repoDir string, ns *node.State, opts Opts) error {
	w, err := ns.Applied()
	if err != nil {
		return err
	}
	head, err := s.Head()
	if err != nil {
		return err
	}

	if head == w {
		return nil
	}

	for seq := w + 1; seq <= head; seq++ {
		e, err := s.ReadEntry(seq)
		if err != nil {
			return err
		}

		if e.PackKey != "" {
			r, err := s.GetBlob(e.PackKey)
			if err != nil {
				return err
			}
			cmd := exec.Command("git", "--git-dir", repoDir, "index-pack", "--stdin")
			cmd.Stdin = r
			// Prevent hooks from triggering during replay
			cmd.Env = append(os.Environ(), "TOY_REPLAY=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				r.Close()
				return fmt.Errorf("seq %d index-pack: %w\n%s", seq, err, out)
			}
			r.Close()
		}

		for _, u := range e.Updates {
			var cmd *exec.Cmd
			if u.New == model.ZeroOID {
				cmd = exec.Command("git", "--git-dir", repoDir, "update-ref", "-d", u.Ref)
			} else {
				cmd = exec.Command("git", "--git-dir", repoDir, "update-ref", u.Ref, u.New)
			}
			cmd.Env = append(os.Environ(), "TOY_REPLAY=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("seq %d update-ref %s: %w\n%s", seq, u.Ref, err, out)
			}
		}

		if err := ns.SetApplied(seq); err != nil {
			return err
		}
	}

	return nil
}
