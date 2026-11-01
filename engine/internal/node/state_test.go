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
	if err := s.SetApplied(7); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Applied(); err != nil || n != 7 {
		t.Fatalf("got %d,%v", n, err)
	}
}
