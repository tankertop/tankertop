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
}
