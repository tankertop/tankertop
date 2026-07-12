package ui

import (
	"strings"
	"testing"
)

func TestIsBinary(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"plain text", []byte("hello\nworld\n"), false},
		{"json", []byte(`{"a":1,"b":"two"}`), false},
		{"empty", []byte(""), false},
		{"elf magic (has NUL)", []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}, true},
		{"control bytes", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x1b}, true},
		{"utf8 text", []byte("café ☕ résumé"), false},
	}
	for _, c := range cases {
		if got := isBinary(c.data); got != c.want {
			t.Errorf("isBinary(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHexDump(t *testing.T) {
	got := hexDump([]byte{0x7f, 'E', 'L', 'F', 0x00, 0x41})
	// offset, hex bytes, ascii gutter with non-printables as dots
	if !strings.HasPrefix(got, "00000000  7f 45 4c 46 00 41 ") {
		t.Errorf("hex bytes wrong:\n%q", got)
	}
	if !strings.Contains(got, "|.ELF.A|") {
		t.Errorf("ascii gutter wrong:\n%q", got)
	}
	// a full 16-byte row plus a partial row
	got = hexDump(make([]byte, 20))
	if lines := strings.Count(got, "\n"); lines != 2 {
		t.Errorf("20 bytes -> %d rows, want 2", lines)
	}
}
