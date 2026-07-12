package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBaseName(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin": "bin",
		"/etc/":          "etc",
		"/etc":           "etc",
		"/":              "",
		"file.txt":       "file.txt",
	}
	for in, want := range cases {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Ascending into a parent should land the cursor on the directory just left,
// via fsSelectName; descending should restore the remembered cursor.
func TestFsListMsgRestoresCursor(t *testing.T) {
	m := New(nil, 0, "")

	// Arrive in /usr, having come up from /usr/local — select that entry.
	m.fsSelectName = "local"
	tm, _ := m.Update(fsListMsg{path: "/usr", entries: []fsEntry{
		{"bin", true}, {"lib", true}, {"local", true}, {"share", true},
	}})
	m = tm.(Model)
	if got := m.fsEntries[m.fsCursor].name; got != "local" {
		t.Fatalf("after ascend, cursor on %q, want local", got)
	}
	if m.fsSelectName != "" {
		t.Error("fsSelectName should be cleared after use")
	}

	// Now remember cursor 2 for /usr, descend elsewhere, come back — restore it.
	m.fsCursorMem["/usr"] = 2
	tm, _ = m.Update(fsListMsg{path: "/usr", entries: []fsEntry{
		{"bin", true}, {"lib", true}, {"local", true}, {"share", true},
	}})
	m = tm.(Model)
	if m.fsCursor != 2 {
		t.Errorf("remembered cursor = %d, want 2", m.fsCursor)
	}

	// A remembered index past the (shorter) new listing clamps in range.
	m.fsCursorMem["/tmp"] = 9
	tm, _ = m.Update(fsListMsg{path: "/tmp", entries: []fsEntry{{"a", false}, {"b", false}}})
	m = tm.(Model)
	if m.fsCursor != 1 {
		t.Errorf("clamped cursor = %d, want 1", m.fsCursor)
	}
}

// Opening a file sets the text view to return to the browser, not the dashboard.
func TestTextReturnFromFile(t *testing.T) {
	m := New(nil, 0, "")
	tm, _ := m.Update(textMsg{title: "file: /etc/hosts", body: "127.0.0.1 localhost", returnTo: viewFiles})
	m = tm.(Model)
	if m.view != viewText || m.textReturn != viewFiles {
		t.Fatalf("view=%v textReturn=%v, want viewText/viewFiles", m.view, m.textReturn)
	}
	// esc returns to the file browser
	tm, _ = m.handleTextKey(tea.KeyMsg{Type: tea.KeyEsc})
	if tm.(Model).view != viewFiles {
		t.Errorf("esc from file view went to %v, want viewFiles", tm.(Model).view)
	}
}
