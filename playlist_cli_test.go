package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

// TestRunKeepsPlaylistsWorking runs tagshelf on a source folder with a
// playlist that names files of two folders: it goes to the playlists folder,
// the default one or the one -playlists names, with every entry naming a
// file that is there. With -no-playlist-rewrite it travels like any other
// file, here after the audio into the one album it all went to, and keeps
// its text.
func TestRunKeepsPlaylistsWorking(t *testing.T) {
	const mix = "#EXTM3U\na/one.flac\nb/two.mp3\n"
	tests := []struct {
		name  string
		flags func(tmp string) []string
		at    func(tmp string) string
		kept  bool // as it was written
		says  []string
	}{
		{"by default", func(string) []string { return nil },
			func(tmp string) string { return filepath.Join(tmp, "lib", "_Playlists", "mix.m3u8") }, false,
			[]string{"Playlists rewritten:      1", "Playlists follow: true", "[PLAYLIST]", "2 of its entries follow their files"}},
		{"a playlists folder of its own", func(tmp string) []string { return []string{"-playlists", filepath.Join(tmp, "lists")} },
			func(tmp string) string { return filepath.Join(tmp, "lists", "mix.m3u8") }, false,
			[]string{"Playlists rewritten:      1"}},
		{"turned off", func(string) []string { return []string{"-no-playlist-rewrite"} },
			func(tmp string) string {
				return filepath.Join(tmp, "lib", "Fixture Album Artist", "Fixture Album (2001)", "mix.m3u8")
			}, true,
			[]string{"Playlists rewritten:      0", "Playlists follow: false"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, config, logs := cliEnv(t)
			writeFile(t, config, "")
			src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
			for name, fixture := range map[string]string{filepath.Join("a", "one.flac"): fixtures.Tagged(".flac"), filepath.Join("b", "two.mp3"): fixtures.Tagged(".mp3")} {
				data, err := os.ReadFile(fixture)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(src, name), string(data))
			}
			writeFile(t, filepath.Join(src, "mix.m3u8"), mix)

			args := append([]string{"-config", config, "-source", src, "-dest", lib, "-dry-run=false", "-verbose"}, tt.flags(tmp)...)
			if got := run(context.Background(), args); got != exitOK {
				t.Fatalf("run(%q) = %d\n%s", args, got, logs)
			}
			for _, want := range tt.says {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("the output lacks %q:\n%s", want, logs)
				}
			}
			list := tt.at(tmp)
			data, err := os.ReadFile(list)
			if err != nil {
				t.Fatalf("the playlist is not at %s: %v\n%s", list, err, logs)
			}
			if tt.kept {
				if string(data) != mix {
					t.Errorf("the playlist was changed: %q", data)
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 3 || lines[0] != "#EXTM3U" {
				t.Fatalf("the playlist holds %q", data)
			}
			for _, entry := range lines[1:] {
				if strings.Contains(entry, `\`) {
					t.Errorf("the entry %q lost its style: it was written with /", entry)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(list), filepath.FromSlash(entry))); err != nil {
					t.Errorf("the entry %q names no file: %v", entry, err)
				}
			}
		})
	}
}
