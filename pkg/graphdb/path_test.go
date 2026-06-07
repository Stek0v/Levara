package graphdb

import (
	"context"
	"strings"
	"testing"
)

func TestEncodeDecodeCursorRoundTrip(t *testing.T) {
	for _, off := range []int{0, 1, 100, 99999} {
		got, err := decodeCursor(encodeCursor(pathCursor{Offset: off}))
		if err != nil {
			t.Fatalf("offset %d: %v", off, err)
		}
		if got.Offset != off {
			t.Errorf("offset %d round-trip: got %d", off, got.Offset)
		}
	}
}

func TestDecodeCursorEmpty(t *testing.T) {
	got, err := decodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor: %v", err)
	}
	if got.Offset != 0 {
		t.Errorf("empty cursor should yield offset 0, got %d", got.Offset)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	cases := []string{
		"not-base64!!",
		"",       // empty handled separately above; here just for completeness
		"!!!",    // bad base64
		"bm90LWpzb24=", // valid b64 of "not-json"
	}
	// skip empty in the loop; it's tested elsewhere
	for _, c := range cases[2:] {
		if _, err := decodeCursor(c); err == nil {
			t.Errorf("expected decode error for %q", c)
		}
	}
}

func TestDecodeCursorNegativeOffset(t *testing.T) {
	bad := encodeCursor(pathCursor{Offset: -1})
	if _, err := decodeCursor(bad); err == nil {
		t.Error("expected error for negative offset")
	}
}

func TestPathBetweenInputValidation(t *testing.T) {
	// PathBetween fails fast on missing required args before touching neo4j,
	// so a nil writer is fine for these branches.
	w := &Writer{}
	cases := []struct {
		name string
		q    PathQuery
		want string
	}{
		{"missing from", PathQuery{To: "x"}, "from and to required"},
		{"missing to", PathQuery{From: "x"}, "from and to required"},
		{"bad cursor", PathQuery{From: "a", To: "b", Cursor: "!!!"}, "cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.PathBetween(context.Background(), tc.q)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestToInt64(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int64(42), 42},
		{int(7), 7},
		{float64(3.14), 3},
		{"nope", 0},
		{nil, 0},
	}
	for _, tc := range cases {
		if got := toInt64(tc.in); got != tc.want {
			t.Errorf("toInt64(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestToInt64Ptr(t *testing.T) {
	if toInt64Ptr(nil) != nil {
		t.Error("nil should return nil pointer")
	}
	got := toInt64Ptr(int64(99))
	if got == nil || *got != 99 {
		t.Errorf("expected *99, got %v", got)
	}
}
