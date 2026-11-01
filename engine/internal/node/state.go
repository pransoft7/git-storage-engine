// Package node is the engine's node-local state: contains info about in-flight pushes
// and how far local repo has caught up with the log.
package node

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"toyengine/internal/model"
)

// Pending is the engine's memory of one in-flight push, written in pre-receive
// and consumed in the reference-transaction hooks. Key - PushID (the
// receive-pack pid, which every hook of the same push shares).
type Pending struct {
	PushID string      `json:"push_id"`
	Entry  model.Entry `json:"entry"` // draft; Entry.Seq stays 0 until published
}

// State lives in a directory on the node's local disk.
type State struct{ dir string }

// Open creates (if required) and returns the state rooted at dir.
func Open(dir string) (*State, error) {
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0o755); err != nil {
		return nil, err
	}
	return &State{dir: dir}, nil
}

func (s *State) pendingPath(pushID string) string {
	return filepath.Join(s.dir, "pending", pushID+".json")
}

// SavePending writes (or modifies by overwrite) the pending record atomically.
func (s *State) SavePending(p Pending) error {
	return writeAtomic(s.pendingPath(p.PushID), p)
}

// LoadPending returns the pending record or nil in absence.
func (s *State) LoadPending(pushID string) (*Pending, error) {
	b, err := os.ReadFile(s.pendingPath(pushID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Pending
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePending removes the record if exists. No error if does not exist.
func (s *State) DeletePending(pushID string) error {
	err := os.Remove(s.pendingPath(pushID))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SetApplied records that the local repo now reflects the log up to seq.
func (s *State) SetApplied(seq uint64) error {
	return writeAtomic(filepath.Join(s.dir, "applied"), seq)
}

// Applied returns the last applied log seq (0 if none).
func (s *State) Applied() (uint64, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, "applied"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

func writeAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
