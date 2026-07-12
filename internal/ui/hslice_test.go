package ui

import "testing"

func TestHslice(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello world", 0, "hello world"},
		{"hello world", 6, "world"},
		{"hello", 5, ""},
		{"hello", 99, ""},
		{"héllo", 1, "éllo"}, // rune-aware, not byte-aware
	}
	for _, c := range cases {
		if got := hslice(c.in, c.n); got != c.want {
			t.Errorf("hslice(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestSeriesMax(t *testing.T) {
	if got := seriesMax([]float64{1, 5, 3}); got != 5 {
		t.Errorf("seriesMax = %v, want 5", got)
	}
	if got := seriesMax(nil); got != 1 { // empty -> 1, avoids divide-by-zero
		t.Errorf("seriesMax(nil) = %v, want 1", got)
	}
}
