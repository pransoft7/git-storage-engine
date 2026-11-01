package node_test

import (
	"reflect"
	"testing"

	"toyengine/internal/model"
	"toyengine/internal/node"
)

func TestPendingRoundTrip(t *testing.T) {
	s, err := node.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.LoadPending("1"); err != nil || p != nil {
		t.Fatalf("want nil,nil got %v,%v", p, err)
	}
	in := node.Pending{PushID: "1", Entry: model.Entry{PushID: "x", PackKey: "packs/a.pack",
		Updates: []model.RefUpdate{{Ref: "refs/heads/main", Old: model.ZeroOID, New: "n"}}}}
	if err := s.SavePending(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.LoadPending("1")
	if err != nil || out == nil {
		t.Fatal(out, err)
	}
	if !reflect.DeepEqual(out.Entry, in.Entry) {
		t.Fatalf("got %+v want %+v", out.Entry, in.Entry)
	}
	if err := s.DeletePending("1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePending("1"); err != nil {
		t.Fatalf("second delete should be fine: %v", err)
	}
	if p, _ := s.LoadPending("1"); p != nil {
		t.Fatal("still there")
	}
}

func TestApplied(t *testing.T) {
	s, _ := node.Open(t.TempDir())
	if n, err := s.Applied(); err != nil || n != 0 {
		t.Fatalf("got %d,%v", n, err)
	}
	for seq := uint64(1); seq <= 7; seq++ {
		if err := s.SetApplied(seq); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Applied(); err != nil || n != 7 {
		t.Fatalf("got %d,%v", n, err)
	}
}

// TestAppliedGap: if seq 3 is missing, Applied() must stop at 2 even though 4 is committed.
func TestAppliedGap(t *testing.T) {
	s, _ := node.Open(t.TempDir())
	for _, seq := range []uint64{1, 2, 4} {
		if err := s.SetApplied(seq); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Applied(); err != nil || n != 2 {
		t.Fatalf("want 2, got %d,%v", n, err)
	}
}

// TestAppliedFillsGap: once the missing seq is committed, Applied() advances past the gap.
func TestAppliedFillsGap(t *testing.T) {
	s, _ := node.Open(t.TempDir())
	for _, seq := range []uint64{1, 2, 4} {
		if err := s.SetApplied(seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetApplied(3); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Applied(); err != nil || n != 4 {
		t.Fatalf("want 4, got %d,%v", n, err)
	}
}

// TestAppliedOutOfOrder: commits arriving out of log order still produce the correct watermark.
func TestAppliedOutOfOrder(t *testing.T) {
	s, _ := node.Open(t.TempDir())
	for _, seq := range []uint64{4, 3, 1, 2} {
		if err := s.SetApplied(seq); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Applied(); err != nil || n != 4 {
		t.Fatalf("want 4, got %d,%v", n, err)
	}
}

// TestSetAppliedIdempotent: calling SetApplied with the same seq twice must not error.
func TestSetAppliedIdempotent(t *testing.T) {
	s, _ := node.Open(t.TempDir())
	for seq := uint64(1); seq <= 3; seq++ {
		if err := s.SetApplied(seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetApplied(3); err != nil {
		t.Fatalf("second SetApplied(3) should be fine: %v", err)
	}
	if n, err := s.Applied(); err != nil || n != 3 {
		t.Fatalf("want 3, got %d,%v", n, err)
	}
}
