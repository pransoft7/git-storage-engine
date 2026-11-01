package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"toyengine/internal/model"
)

// LocalDirStore keeps everything under one root directory:
//
//	<root>/packs/<sha>.pack        immutable blobs
//	<root>/log/00000001.json       one file per log entry (immutable)
//
// Design: claiming a log slot must be atomic, and the entry must appear
// complete or not at all.
//   - Fsync the contents as a temp file in the same directory (link is atomic within 
//		one filesystem) and link it to log file name and sync the directory.
//   - The temp file iss always removed afterwards, so a
//     lost attempt leaves nothing behind.
//
// MemStore.TryAppend in mem.go is the same logic with a mutex.

// LocalDirStore implements Store on a local directory.
type LocalDirStore struct{ root string }

// NewLocalDirStore opens (creating if needed) a store rooted at root.
func NewLocalDirStore(root string) (*LocalDirStore, error) {
	for _, d := range []string{"packs", "log"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	return &LocalDirStore{root: root}, nil
}

// blobPath maps a blob key like "packs/<sha>.pack" to a file path.
func (s *LocalDirStore) blobPath(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

// logDir is the directory holding the log slot files.
func (s *LocalDirStore) logDir() string { return filepath.Join(s.root, "log") }

// slotPath is the file path of log slot seq (e.g. log/00000004.json).
func (s *LocalDirStore) slotPath(seq uint64) string {
	return filepath.Join(s.logDir(), SlotName(seq))
}

// writeTemp writes r to a new fsynced temp file in dir and returns its path (removed again on error).
func writeTemp(dir string, r io.Reader) (string, error) {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// syncDir fsyncs a directory so names created/renamed/linked in it survive a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// PutBlob atomically stores r under key (temp file, fsync, rename, fsync dir).
func (s *LocalDirStore) PutBlob(key string, r io.Reader) error {
	path := s.blobPath(key)
	dir := filepath.Dir(path)

	tmp, err := writeTemp(dir, r)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

// GetBlob opens the stored blob for reading; a missing blob returns an fs.ErrNotExist error.
func (s *LocalDirStore) GetBlob(key string) (io.ReadCloser, error) {
	blob, err := os.Open(s.blobPath(key))
	if err != nil {
		return nil, err
	}
	return blob, nil
}

// Head returns the highest slot number in the log directory, ignoring junk files (0 if empty).
func (s *LocalDirStore) Head() (uint64, error) {
	logs, err := os.ReadDir(s.logDir())
	if err != nil {
		return 0, err
	}
	var headLog uint64
	for _, f := range logs {
		seq, ok := ParseSlotName(f.Name())
		if !ok {
			continue
		}
		headLog = max(headLog, seq)
	}
	return headLog, nil
}

// ReadEntry loads and decodes log slot seq; a missing slot returns an fs.ErrNotExist error.
func (s *LocalDirStore) ReadEntry(seq uint64) (model.Entry, error) {
	var entry model.Entry
	data, err := os.ReadFile(s.slotPath(seq))
	if err != nil {
		return entry, err
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return model.Entry{}, err // prevents sending partially filled entry with type error
	}
	return entry, nil
}

// TryAppend claims slot e.Seq - it writes the entry to a temp file, then os.Link gives it the
// slot name which fails atomically if the slot is taken.
//
// Limitation (v1): if the directory fsync fails after a successful Link, we return an error but
// leave the slot since others may already have built on it. The log then holds a phantom entry for a
// ref move Git never made, and later pushes to that ref hit wal.ErrConflict until reconciled by hand.
func (s *LocalDirStore) TryAppend(e model.Entry) (bool, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return false, err
	}

	tmp, err := writeTemp(s.logDir(), bytes.NewReader(data))
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp) // drops the temp name only, a won slot keeps the data under its own name

	if err := os.Link(tmp, s.slotPath(e.Seq)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil // lost the race, not an error
		}
		return false, err
	}
	if err := syncDir(s.logDir()); err != nil {
		return false, err
	}
	return true, nil
}

var _ Store = (*LocalDirStore)(nil)
