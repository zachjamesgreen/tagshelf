package fixtures

import (
	"path/filepath"
	"testing"
)

// TestOSPath pins what OSPath does off Windows, and that it leaves Windows alone.
func TestOSPath(t *testing.T) {
	tests := map[string]string{
		`D:\Music`:                            "/d/Music",
		`D:\`:                                 "/d/",
		`E:\Rips\Geogaddi (2002)\01 - A.flac`: "/e/Rips/Geogaddi (2002)/01 - A.flac",
		`A\b.mp3`:                             "A/b.mp3",
		`run 5: E:\Incoming -> D:\Music`:      "run 5: /e/Incoming -> /d/Music",
		`"E:\New sq1`:                         `"/e/New sq1`,
		`%LOCALAPPDATA%\tagshelf`:             "%LOCALAPPDATA%/tagshelf",
		"0.0 files/s":                         "0.0 files/s",
		"no toast after R:\nnext":             "no toast after R:\nnext",
	}
	for in, want := range tests {
		if filepath.Separator == '\\' {
			want = in
		}
		got := OSPath(in)
		if got != want {
			t.Errorf("OSPath(%q) = %q, want %q", in, got, want)
		}
		if len(got) != len(in) {
			t.Errorf("OSPath(%q) = %q, which is not as long", in, got)
		}
	}
}
