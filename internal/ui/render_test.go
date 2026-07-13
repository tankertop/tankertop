package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A body line must occupy exactly one terminal row and exactly `width` columns.
// Pod specs contain env values with trailing newlines (OPENSEARCH_JAVA_OPTS is a
// real example); before this was handled, such a line made its box one row too
// tall and pushed the rest of the dashboard off the screen.
func TestFitIsAlwaysOneRowOfExactWidth(t *testing.T) {
	cases := []string{
		"plain",
		"trailing newline\n",
		"embedded\nnewline",
		"carriage\r\nreturn",
		"tab\tseparated",
		strings.Repeat("long", 40),
		"",
	}
	for _, in := range cases {
		got := fit(in, 20)
		if h := lipgloss.Height(got); h != 1 {
			t.Errorf("fit(%q, 20) rendered %d rows, want 1", in, h)
		}
		if w := lipgloss.Width(got); w != 20 {
			t.Errorf("fit(%q, 20) is %d columns wide, want 20", in, w)
		}
	}
}

// A box must be exactly as tall as it was asked to be, whatever the body holds.
func TestBoxHeightIsExact(t *testing.T) {
	body := []string{"ok", "OPENSEARCH_JAVA_OPTS=-Xms1121m -Xmx1121m\n", "after"}
	got := box("title", body, 30, 8)
	if h := lipgloss.Height(got); h != 8 {
		t.Errorf("box height = %d, want 8", h)
	}
}

func TestEscapeCtl(t *testing.T) {
	if got, want := escapeCtl("-Xms1121m\n"), `-Xms1121m\n`; got != want {
		t.Errorf("escapeCtl = %q, want %q", got, want)
	}
	if got := escapeCtl("no controls"); got != "no controls" {
		t.Errorf("escapeCtl altered a clean string: %q", got)
	}
	// An env value carrying an OSC 52 clipboard-write must be neutralised.
	if got := escapeCtl("x\x1b]52;c;cm0=\x07y"); strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) {
		t.Errorf("escapeCtl left an escape sequence intact: %q", got)
	}
}

func TestSanitizeStripsTerminalControl(t *testing.T) {
	// A hostile container's OSC 52 payload (clipboard hijack) and CSI screen
	// clears must not survive to the terminal.
	cases := []string{
		"log \x1b]52;c;cm0gLXJmIH4=\x07 line", // OSC 52 clipboard write
		"\x1b[2J\x1b[H fake ui",                // clear screen + home
		"title\x1b]0;spoofed\x07",              // window-title spoof
		"bell\x07 and \x08backspace",           // BEL + backspace
		"lone esc \x1b here",                   // stray ESC
	}
	for _, in := range cases {
		got := sanitize(in)
		for _, r := range got {
			if isControl(r) {
				t.Errorf("sanitize(%q) left control byte %#x in %q", in, r, got)
			}
		}
	}
	// Whitespace that downstream code relies on is preserved.
	if got := sanitize("a\nb\tc"); got != "a\nb\tc" {
		t.Errorf("sanitize stripped whitespace: %q", got)
	}
	// A clean string is returned unchanged (and cheaply).
	if got := sanitize("normal text 123"); got != "normal text 123" {
		t.Errorf("sanitize altered a clean string: %q", got)
	}
}
