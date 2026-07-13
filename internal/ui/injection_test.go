package ui

import "testing"

// hasControl reports whether s still contains a terminal control byte that a
// hostile container could use to hijack the clipboard or forge the display.
func hasControl(s string) bool {
	for _, r := range s {
		if isControl(r) {
			return true
		}
	}
	return false
}

// The OSC 52 clipboard-write and CSI screen-clear a hostile container might emit
// must not survive ingestion into the log/text/file-listing state.
const osc52 = "\x1b]52;c;cm0gLXJmIH4=\x07"

func TestLogIngestionSanitizes(t *testing.T) {
	out, _ := Model{}.Update(logsMsg{body: "line one " + osc52 + " x\nline two \x1b[2J y"})
	nm := out.(Model)
	if len(nm.logBuf) == 0 {
		t.Fatal("no log lines captured")
	}
	for _, l := range nm.logBuf {
		if hasControl(l) {
			t.Errorf("log line retained a control byte: %q", l)
		}
	}
}

func TestTextViewSanitizes(t *testing.T) {
	out, _ := Model{}.Update(textMsg{body: "harmless readme " + osc52 + " more text"})
	nm := out.(Model)
	for _, l := range nm.textLines {
		if hasControl(l) {
			t.Errorf("text line retained a control byte: %q", l)
		}
	}
}

func TestFileListingSanitizesNames(t *testing.T) {
	// A file literally named with an embedded OSC 52 payload, plus a clean dir.
	entries := parseLsEntries("fnotes" + osc52 + "txt\ndgood")
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if hasControl(e.name) {
			t.Errorf("entry name retained a control byte: %q", e.name)
		}
	}
}
