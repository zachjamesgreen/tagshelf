package playlist

import (
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestIs(t *testing.T) {
	for name, want := range map[string]bool{"a.m3u": true, "A.M3U8": true, "a.m3u.orig": false, "a.cue": false, "m3u": false} {
		if got := Is(name); got != want {
			t.Errorf("Is(%q) = %t, want %t", name, got, want)
		}
	}
}

// A playlist is written back as it was read, byte for byte, whatever its
// line endings, byte order mark, white space and encoding.
func TestBytesAreAsRead(t *testing.T) {
	tests := map[string]struct{ name, data string }{
		"unix lines":          {"a.m3u8", "#EXTM3U\n#EXTINF:1,One\none.mp3\n"},
		"windows lines":       {"a.m3u", "#EXTM3U\r\n\r\none.mp3\r\ntwo.mp3"},
		"mixed, no last line": {"a.m3u", "one.mp3\r\ntwo.mp3\n  three.mp3 \t\n\n"},
		"a byte order mark":   {"a.m3u8", "\xef\xbb\xbf#EXTM3U\nsång.mp3\n"},
		"windows-1252":        {"a.m3u", "s\xe5ng \x80 \x81.mp3\r\n"},
		"empty":               {"a.m3u", ""},
	}
	for name, tt := range tests {
		got, err := Parse([]byte(tt.data), tt.name).Bytes()
		if err != nil || string(got) != tt.data {
			t.Errorf("%s: written back as %q (%v), want %q", name, got, err, tt.data)
		}
	}
}

func TestEntries(t *testing.T) {
	l := Parse([]byte("#EXTM3U\r\n#EXTINF:1,One\r\n one.mp3 \r\n\r\nhttp://example.com/stream\r\nC:\\Music\\two.mp3\r\n# three.mp3\r\n"), "a.m3u")
	want := []Entry{{Line: 2, Text: "one.mp3"}, {Line: 5, Text: `C:\Music\two.mp3`}}
	if got := l.Entries(); !reflect.DeepEqual(got, want) {
		t.Errorf("Entries = %+v, want %+v", got, want)
	}
	if got := l.NotFiles(); got != 1 {
		t.Errorf("NotFiles = %d, want the one address", got)
	}
	l.Set(2, "Sub/1.mp3")
	got, err := l.Bytes()
	const rewritten = "#EXTM3U\r\n#EXTINF:1,One\r\n Sub/1.mp3 \r\n\r\nhttp://example.com/stream\r\nC:\\Music\\two.mp3\r\n# three.mp3\r\n"
	if err != nil || string(got) != rewritten {
		t.Errorf("after Set: %q (%v), want %q", got, err, rewritten)
	}
}

// A .m3u that is not UTF-8 is Windows-1252, and takes only the characters
// that encoding has. A .m3u8, and a .m3u that is UTF-8, take any.
func TestEncodings(t *testing.T) {
	latin := Parse([]byte("s\xe5ng.mp3\n"), "a.m3u")
	if got := latin.Entries(); len(got) != 1 || got[0].Text != "sång.mp3" {
		t.Fatalf("a Windows-1252 entry was read as %+v", got)
	}
	latin.Set(0, "Zoë – €.mp3")
	if got, err := latin.Bytes(); err != nil || string(got) != "Zo\xeb \x96 \x80.mp3\n" {
		t.Errorf("written in Windows-1252: %q (%v)", got, err)
	}
	latin.Set(0, "日本.mp3")
	if _, err := latin.Bytes(); !errors.Is(err, ErrEncoding) {
		t.Errorf("a name Windows-1252 cannot hold: %v, want ErrEncoding", err)
	}

	for _, name := range []string{"a.m3u", "a.m3u8"} {
		l := Parse([]byte("sång.mp3\n"), name)
		l.Set(0, "日本.mp3")
		if got, err := l.Bytes(); err != nil || string(got) != "日本.mp3\n" {
			t.Errorf("%s in UTF-8: %q (%v)", name, got, err)
		}
	}
	// A .m3u8 that is not valid UTF-8 is left as its bytes are.
	if got, err := Parse([]byte("s\xe5ng.mp3\n"), "a.m3u8").Bytes(); err != nil || string(got) != "s\xe5ng.mp3\n" {
		t.Errorf("a .m3u8 that is not UTF-8: %q (%v)", got, err)
	}
}

func TestIsURL(t *testing.T) {
	for text, want := range map[string]bool{
		"http://example.com/a.mp3": true, "file:///home/a.mp3": true, "mms://x": true,
		`C:\Music\a.mp3`: false, "C:/Music/a.mp3": false, "a.mp3": false, "Sub/a.mp3": false, "/home/a.mp3": false,
	} {
		if got := IsURL(text); got != want {
			t.Errorf("IsURL(%q) = %t, want %t", text, got, want)
		}
	}
}

func TestResolveAndFormat(t *testing.T) {
	type test struct {
		dir, text, want string
		style           Style
	}
	var tests []test
	var lost []string
	if runtime.GOOS == "windows" {
		tests = []test{
			{`D:\Lists`, "one.mp3", `D:\Lists\one.mp3`, Style{}},
			{`D:\Lists`, `Sub\one.mp3`, `D:\Lists\Sub\one.mp3`, Style{Backslash: true}},
			{`D:\Lists`, "../Artist/Album/one.mp3", `D:\Artist\Album\one.mp3`, Style{}},
			{`D:\Lists`, `..\Artist\one.mp3`, `D:\Artist\one.mp3`, Style{Backslash: true}},
			{`D:\Lists`, `E:\Music\one.mp3`, `E:\Music\one.mp3`, Style{Absolute: true, Backslash: true}},
			{`D:\Lists`, "E:/Music/one.mp3", `E:\Music\one.mp3`, Style{Absolute: true}},
			{`D:\Lists`, `\Music\one.mp3`, `D:\Music\one.mp3`, Style{Absolute: true, Rooted: true, Backslash: true}},
			{`D:\Lists`, `\\nas\music\one.mp3`, `\\nas\music\one.mp3`, Style{Absolute: true, Backslash: true}},
		}
		lost = []string{"E:one.mp3"}
	} else {
		tests = []test{
			{"/lists", "one.mp3", "/lists/one.mp3", Style{}},
			{"/lists", `Sub\one.mp3`, "/lists/Sub/one.mp3", Style{Backslash: true}},
			{"/lists", "../Artist/Album/one.mp3", "/Artist/Album/one.mp3", Style{}},
			{"/lists", "/music/one.mp3", "/music/one.mp3", Style{Absolute: true}},
		}
		lost = []string{`E:\Music\one.mp3`, "E:/Music/one.mp3", `\\nas\music\one.mp3`}
	}
	for _, tt := range tests {
		if got := StyleOf(tt.text); got != tt.style {
			t.Errorf("StyleOf(%q) = %+v, want %+v", tt.text, got, tt.style)
		}
		got, ok := Resolve(tt.dir, tt.text)
		if !ok || got != tt.want {
			t.Errorf("Resolve(%q, %q) = %q, %t, want %q", tt.dir, tt.text, got, ok, tt.want)
		}
		// Written again in its own style, an entry is as it was.
		if back := Format(tt.dir, tt.want, tt.style); back != tt.text {
			t.Errorf("Format(%q, %q, %+v) = %q, want %q", tt.dir, tt.want, tt.style, back, tt.text)
		}
	}
	for _, text := range lost {
		if got, ok := Resolve(filepath.Dir(tests[0].want), text); ok {
			t.Errorf("Resolve(%q) = %q, want it to name no file here", text, got)
		}
	}
}

// A file on another drive than the playlist is written whole.
func TestFormatAcrossDrives(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drives are a thing of Windows")
	}
	if got := Format(`D:\Lists`, `E:\Music\one.mp3`, Style{Backslash: true}); got != `E:\Music\one.mp3` {
		t.Errorf("from D: to E: = %q", got)
	}
	if got := Format(`D:\Lists`, `E:\Music\one.mp3`, Style{Absolute: true, Rooted: true}); got != "E:/Music/one.mp3" {
		t.Errorf("a path without a drive, to another drive = %q", got)
	}
}
