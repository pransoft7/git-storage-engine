// Command toyengine is the CLI that Git hooks call. It translates a hook
// invocation (stdin, args, environment, pid) into calls on the coordinator and
// translates the result back into an exit code.
//
//	toyengine hook <hook-name> [hook args...]        called by hooks/toyhook
//	toyengine inspect <repo>                         dump the log of a repo
//	toyengine replay <repo> --into <dir>             replay the log into a bare repo
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"toyengine/internal/coordinator"
	"toyengine/internal/hookio"
	"toyengine/internal/node"
	"toyengine/internal/replay"
	"toyengine/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "hook":
		if len(os.Args) < 3 {
			usage()
		}
		os.Exit(runHook(os.Args[2], os.Args[3:]))
	case "inspect":
		if len(os.Args) != 3 {
			usage()
		}
		os.Exit(runInspect(os.Args[2]))
	case "replay":
		os.Exit(runReplay(os.Args[2:]))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: toyengine hook <name> [args...] | toyengine inspect <repo> | toyengine replay <repo> --into <dir>")
	os.Exit(2)
}

// dataDir is the engine's root: <dir>/store/<repo> is the shared "S3",
// <dir>/node/<repo> is node-local state.
func dataDir() string {
	if d := os.Getenv("TOYENGINE_DIR"); d != "" {
		return d
	}
	return "/var/lib/toyengine"
}

// repoName derives the repo name from GIT_DIR ("." inside push hooks, where
// the working directory is the bare repo): /srv/git/demo.git -> "demo".
func repoName() (string, error) {
	gd := os.Getenv("GIT_DIR")
	if gd == "" {
		gd = "."
	}
	abs, err := filepath.Abs(gd)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(filepath.Base(abs), ".git"), nil
}

func newLogf(repo, hook string) func(string, ...any) {
	return func(format string, args ...any) {
		f, err := os.OpenFile(filepath.Join(dataDir(), "engine.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s pid=%d repo=%s hook=%s %s\n", time.Now().UTC().Format("15:04:05.000"),
			os.Getpid(), repo, hook, fmt.Sprintf(format, args...))
	}
}

func runHook(name string, args []string) int {
	// Replay sets TOY_REPLAY=1 so that engine hooks installed in the target repo
	// are no-ops: replay drives ref updates itself and must not re-publish them.
	if os.Getenv("TOY_REPLAY") == "1" {
		return 0
	}
	if name != "pre-receive" && name != "reference-transaction" {
		return 0 // not a hook we care about
	}
	repo, err := repoName()
	if err != nil {
		fmt.Fprintln(os.Stderr, "toyengine:", err)
		return 1
	}
	logf := newLogf(repo, name)

	st, err := store.NewLocalDirStore(filepath.Join(dataDir(), "store", repo))
	if err != nil {
		fmt.Fprintln(os.Stderr, "toyengine:", err)
		return 1
	}
	ns, err := node.Open(filepath.Join(dataDir(), "node", repo))
	if err != nil {
		fmt.Fprintln(os.Stderr, "toyengine:", err)
		return 1
	}
	c := &coordinator.Coordinator{Store: st, Node: ns, Logf: logf}
	pushID := strconv.Itoa(os.Getppid()) // hook's parent = git receive-pack

	switch name {
	case "pre-receive":
		updates, err := hookio.ParseUpdates(os.Stdin)
		if err != nil {
			logf("bad stdin: %v", err)
			fmt.Fprintln(os.Stderr, "toyengine:", err)
			return 1
		}
		err = c.PreReceive(coordinator.PreReceiveInput{
			PushID: pushID, Updates: updates, QuarantinePath: os.Getenv("GIT_QUARANTINE_PATH"),
		})
		var rej *coordinator.RejectError
		switch {
		case errors.As(err, &rej):
			logf("rejected: %s", rej.Msg)
			fmt.Fprintln(os.Stderr, "toyengine: push rejected:", rej.Msg)
			return 1
		case err != nil:
			logf("pre-receive failed: %v", err)
			fmt.Fprintln(os.Stderr, "toyengine: internal error:", err)
			return 1
		}
		logf("pre-receive ok push=%s", pushID)

	case "reference-transaction":
		if len(args) != 1 {
			return 0
		}
		switch args[0] {
		case "prepared":
			// An error here aborts the ref update, and the client may hang or see a
			// disconnect (experiment case C; the packed-delete run). Log loudly.
			if err := c.Prepared(pushID); err != nil {
				logf("PREPARED FAILED push=%s: %v", pushID, err)
				fmt.Fprintln(os.Stderr, "toyengine: prepared failed:", err)
				return 1
			}
		case "committed":
			if err := c.Committed(pushID); err != nil {
				logf("committed handler error (ignored by git): %v", err)
			}
		case "aborted":
			if err := c.Aborted(pushID); err != nil {
				logf("aborted handler error (ignored by git): %v", err)
			}
		}
	}
	return 0
}

func runReplay(args []string) int {
	// parse: <repo> --into <dir>
	var repo, intoDir string
	if len(args) >= 1 {
		repo = args[0]
	}
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--into" {
			intoDir = args[i+1]
			break
		}
	}
	if repo == "" || intoDir == "" {
		fmt.Fprintln(os.Stderr, "usage: toyengine replay <repo> --into <dir>")
		return 2
	}
	if _, err := os.Stat(filepath.Join(intoDir, "HEAD")); err != nil {
		fmt.Fprintf(os.Stderr, "toyengine: %s is not a bare git repo (run: git init --bare %s)\n", intoDir, intoDir)
		return 1
	}
	st, err := store.NewLocalDirStore(filepath.Join(dataDir(), "store", repo))
	if err != nil {
		fmt.Fprintln(os.Stderr, "toyengine:", err)
		return 1
	}
	// Node state tracks replay progress for this target repo; lives inside it.
	ns, err := node.Open(filepath.Join(intoDir, "toyengine"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "toyengine:", err)
		return 1
	}
	if err := replay.Apply(st, intoDir, ns, replay.Opts{}); err != nil {
		fmt.Fprintln(os.Stderr, "toyengine: replay failed:", err)
		return 1
	}
	w, _ := ns.Applied()
	fmt.Printf("replay: %s replayed through seq %d\n", repo, w)
	return 0
}

func runInspect(repo string) int {
	// Read-only: never create directories. Whoever creates them owns them, and the hooks
	// (running as www-data) must be the ones to do it, not an admin running inspect as root.
	root := filepath.Join(dataDir(), "store", repo)
	if _, err := os.Stat(filepath.Join(root, "log")); err != nil {
		fmt.Fprintf(os.Stderr, "no log for repo %q under %s (nothing pushed yet, or wrong name)\n", repo, root)
		return 1
	}
	st, err := store.NewLocalDirStore(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	head, err := st.Head()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("# repo=%s head=%d\n", repo, head)
	enc := json.NewEncoder(os.Stdout)
	for seq := uint64(1); seq <= head; seq++ {
		e, err := st.ReadEntry(seq)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = enc.Encode(e)
	}
	return 0
}
