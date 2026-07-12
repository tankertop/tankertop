package ui

import "testing"

func TestPathJoin(t *testing.T) {
	cases := []struct{ dir, child, want string }{
		{"/", "etc", "/etc"},
		{"/etc", "hosts", "/etc/hosts"},
		{"/etc/", "hosts", "/etc/hosts"},
		{"/etc", "..", "/"},
		{"/", "..", "/"},
		{"/usr/local/bin", "..", "/usr/local"},
		{"/usr", "..", "/"},
	}
	for _, c := range cases {
		if got := pathJoin(c.dir, c.child); got != c.want {
			t.Errorf("pathJoin(%q, %q) = %q, want %q", c.dir, c.child, got, c.want)
		}
	}
}

func TestParseLsEntriesDirsFirst(t *testing.T) {
	// type-prefixed: 'd'/'f' + name (symlink-to-dir also comes through as 'd')
	out := "fzzz.txt\ndbin\ndetc\nfalpine-release\ndapk\n\n"
	got := parseLsEntries(out)
	want := []fsEntry{
		{"apk", true}, {"bin", true}, {"etc", true}, // dirs, alpha
		{"alpine-release", false}, {"zzz.txt", false}, // files, alpha
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
