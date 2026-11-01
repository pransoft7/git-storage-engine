package store

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"sync"

	"toyengine/internal/model"
)

// MemStore is an in-memory Store used as a fake in tests of the layers above
// (wal, coordinator). It also serves as a reference implementation of the
// contract.
type MemStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
	slots map[uint64]model.Entry
}

func NewMemStore() *MemStore {
	return &MemStore{blobs: map[string][]byte{}, slots: map[uint64]model.Entry{}}
}

func (m *MemStore) PutBlob(key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = b
	return nil
}

func (m *MemStore) GetBlob(key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[key]
	if !ok {
		return nil, fmt.Errorf("blob %q: %w", key, fs.ErrNotExist)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *MemStore) Head() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var head uint64
	for seq := range m.slots {
		head = max(head, seq)
	}
	return head, nil
}

func (m *MemStore) ReadEntry(seq uint64) (model.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.slots[seq]
	if !ok {
		return model.Entry{}, fmt.Errorf("entry %d: %w", seq, fs.ErrNotExist)
	}
	return e, nil
}

// TryAppend: the CAS operation, made atomic here by the mutex. On disk the same job is done by link.
func (m *MemStore) TryAppend(e model.Entry) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.slots[e.Seq]; taken {
		return false, nil
	}
	m.slots[e.Seq] = e
	return true, nil
}
