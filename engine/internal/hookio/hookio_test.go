package hookio_test

import (
	"reflect"
	"strings"
	"testing"

	"toyengine/internal/hookio"
	"toyengine/internal/model"
)

func TestParseUpdates(t *testing.T) {
	z := model.ZeroOID
	a := strings.Repeat("a", 40)
	in := z + " " + a + " refs/heads/main\n" + z + " " + a + " HEAD\n\n"
	got, err := hookio.ParseUpdates(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []model.RefUpdate{
		{Ref: "refs/heads/main", Old: z, New: a},
		{Ref: "HEAD", Old: z, New: a},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseUpdatesSymbolic(t *testing.T) {
	got, err := hookio.ParseUpdates(strings.NewReader("ref:refs/heads/a ref:refs/heads/b HEAD\n"))
	if err != nil || len(got) != 1 || got[0].New != "ref:refs/heads/b" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseUpdatesMalformed(t *testing.T) {
	if _, err := hookio.ParseUpdates(strings.NewReader("only two\n")); err == nil {
		t.Fatal("want error")
	}
}

func TestParseUpdatesEmpty(t *testing.T) {
	got, err := hookio.ParseUpdates(strings.NewReader(""))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}
