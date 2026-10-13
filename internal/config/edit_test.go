package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

// TestUpdateTheExampleFile changes settings in the example config file and
// checks that only their lines change, and that each keeps its comment.
func TestUpdateTheExampleFile(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Update(example, map[string]any{
		"destination":         `E:\My Music`,
		"dry_run":             false,
		"untagged":            `E:\Loose "stuff"`,
		"workers":             4,
		"musicbrainz_contact": "me@example.org",
		"cue_rewrite":         true, // as it is: the line is written again all the same
	})
	if err != nil {
		t.Fatal(err)
	}
	before, after := strings.Split(string(example), "\n"), strings.Split(string(got), "\n")
	if len(before) != len(after) {
		t.Fatalf("the file has %d lines, had %d:\n%s", len(after), len(before), got)
	}
	changed := map[string]string{}
	for i := range before {
		if before[i] != after[i] {
			key, _, _ := strings.Cut(strings.TrimPrefix(before[i], "#"), ":")
			changed[key] = after[i]
		}
	}
	want := map[string]string{
		"destination":         `destination: "E:\\My Music"               # organized library`,
		"dry_run":             `dry_run: false                          # preview only; set to false to move files`,
		"untagged":            `untagged: "E:\\Loose \"stuff\""      # audio without usable tags (default: <destination>/_Untagged)`,
		"workers":             `workers: 4                             # 0 = number of CPUs`,
		"musicbrainz_contact": `musicbrainz_contact: "me@example.org" # email or URL MusicBrainz can reach you at; lookups are disabled until set`,
	}
	if !reflect.DeepEqual(changed, want) {
		t.Errorf("changed lines:\n%q\nwant\n%q", changed, want)
	}

	var cfg Config
	if err := parse(got, &cfg); err != nil {
		t.Fatal(err)
	}
	on := true
	wantCfg := Config{Source: `D:\Incoming`, Destination: `E:\My Music`, Untagged: `E:\Loose "stuff"`, AutoResolveExact: &on,
		Compilations: "Various Artists", Workers: 4, CueRewrite: &on, MusicBrainzContact: "me@example.org", GenreFromMusicBrainz: "fill"}
	if !reflect.DeepEqual(cfg, wantCfg) {
		t.Errorf("the file reads back as %+v\nwant              %+v", cfg, wantCfg)
	}
}

func TestUpdate(t *testing.T) {
	tests := []struct {
		name string
		file string
		set  map[string]any
		want string
	}{
		{"a value in single quotes", "source: 'D:\\In'  # where it's from\n", map[string]any{"source": `D:\New`},
			"source: \"D:\\\\New\"  # where it's from\n"},
		{"a quote doubled inside single quotes", "source: 'D:\\It''s' # c\n", map[string]any{"source": "x"}, "source: \"x\" # c\n"},
		{"a value without quotes", "workers: 3 # some\ndry_run: true\n", map[string]any{"workers": 0, "dry_run": false},
			"workers: 0 # some\ndry_run: false\n"},
		{"a hash inside quotes is not a comment", "compilations: \"A # B\" # the name\n", map[string]any{"compilations": "C"},
			"compilations: \"C\" # the name\n"},
		{"an escaped quote inside double quotes", "compilations: \"A \\\" # B\"\n", map[string]any{"compilations": "C"}, "compilations: \"C\"\n"},
		{"a hash without a space before it is part of the value", "compilations: A#B\n", map[string]any{"compilations": "C"}, "compilations: \"C\"\n"},
		{"a key with no value", "source:\ndestination: # none yet\n", map[string]any{"source": "s", "destination": "d"},
			"source: \"s\"\ndestination: \"d\" # none yet\n"},
		{"a key the file lacks goes at the end, in the order of the keys", "# mine\ndry_run: true\n",
			map[string]any{"workers": 2, "source": "s"}, "# mine\ndry_run: true\nsource: \"s\"\nworkers: 2\n"},
		{"a key commented out comes back on its line", "#  database: \"C:\\\\old.db\"   # where\ndry_run: true\n",
			map[string]any{"database": `C:\new.db`}, "database: \"C:\\\\new.db\"   # where\ndry_run: true\n"},
		{"an empty file", "", map[string]any{"dry_run": true}, "dry_run: true\n"},
		{"only comments", "# nothing yet\n", map[string]any{"dry_run": true}, "# nothing yet\ndry_run: true\n"},
		{"no line break at the end", "dry_run: true", map[string]any{"dry_run": false, "workers": 1}, "dry_run: false\nworkers: 1\n"},
		{"Windows line breaks are kept", "source: a\r\n# c\r\ndry_run: true\r\n", map[string]any{"source": "b", "verbose": true},
			"source: \"b\"\r\n# c\r\ndry_run: true\r\nverbose: true\r\n"},
		{"nothing to set", "source: a # c\n", nil, "source: a # c\n"},
		{"an empty value is written, not removed", "untagged: \"D:\\\\U\"\n", map[string]any{"untagged": ""}, "untagged: \"\"\n"},
		{"indented under nothing, spaces around the colon kept before it", "workers:    3\n", map[string]any{"workers": 5}, "workers: 5\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Update([]byte(tt.file), tt.set)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Update gave\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestUpdateRefuses checks the files and values Update will not write: it
// returns an error, and nothing to put in the file.
func TestUpdateRefuses(t *testing.T) {
	tests := []struct {
		name string
		file string
		set  map[string]any
		want string
	}{
		{"a value over several lines", "source: |\n  D:\\In\ndry_run: true\n", map[string]any{"source": "x"}, "written over several lines"},
		{"a folded value", "source: >\n  D:\\In\n", map[string]any{"source": "x"}, "written over several lines"},
		{"a value on the next line", "source:\n  D:\\In\n", map[string]any{"source": "x"}, "written over several lines"},
		{"a value that is a list", "workers: [1, 2]\n", map[string]any{"workers": 3}, "written over several lines"},
		{"a file that is a list", "- a\n- b\n", map[string]any{"workers": 3}, "not a list of settings"},
		{"a file that is not YAML", "source: [\n", map[string]any{"workers": 3}, "not valid YAML"},
		{"a key that is no setting", "", map[string]any{"dry-run": true}, `there is no setting "dry-run"`},
		{"a line break in a value", "", map[string]any{"source": "a\nb"}, "cannot be typed"},
		{"a value of the wrong kind", "", map[string]any{"workers": 1.5}, "a setting cannot be float64"},
		{"a file with an unknown key", "dry-run: true\n", map[string]any{"workers": 3}, "could not be changed safely"},
		{"a value that would not read back", "", map[string]any{"workers": "three"}, "could not be changed safely"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Update([]byte(tt.file), tt.set)
			if err == nil || !strings.Contains(err.Error(), tt.want) || got != nil {
				t.Errorf("Update = %q, %v; want an error saying %q and no text", got, err, tt.want)
			}
		})
	}
}

// TestValuesSetChanged checks settings by key: every key has a value of its
// kind, Set puts one back, and Changed names what differs.
func TestValuesSetChanged(t *testing.T) {
	cfg := wantEveryKey()
	values := cfg.Values()
	if len(values) != len(Keys()) {
		t.Fatalf("Values has %d settings, want one for each of the %d keys", len(values), len(Keys()))
	}
	var built Config
	for _, key := range Keys() {
		var err error
		if built, err = built.Set(key, values[key]); err != nil {
			t.Fatalf("Set(%q, %v): %v", key, values[key], err)
		}
	}
	if !reflect.DeepEqual(built.Values(), values) || len(built.Changed(cfg)) != 0 {
		t.Errorf("the config built from its values is %+v, want %+v", built, cfg)
	}

	// The two settings that are on unless turned off read as on when unset,
	// and unset is not a change from on.
	on := true
	if v := (Config{}).Values(); v["cue_rewrite"] != true || v["auto_resolve_exact"] != true {
		t.Errorf("an empty config's values = %v, want cue_rewrite and auto_resolve_exact on", v)
	}
	if got := (Config{}).Changed(Config{CueRewrite: &on}); len(got) != 0 {
		t.Errorf("Changed between unset and on = %v, want nothing", got)
	}
	if got := (Config{Source: "a", Workers: 1}).Changed(Config{Source: "b", DryRun: true, Workers: 1}); !reflect.DeepEqual(got, []string{"source", "dry_run"}) {
		t.Errorf("Changed = %v, want source and dry_run", got)
	}

	for key, value := range map[string]any{"workers": "3", "dry_run": 1, "source": true, "cue_rewrite": "yes", "nope": 1, "verbose": nil} {
		if _, err := (Config{}).Set(key, value); err == nil {
			t.Errorf("Set(%q, %v) succeeded", key, value)
		}
	}
}

// TestWriteFile checks the file is made with its folder, replaced when it is
// there, and that nothing else is left beside it.
func TestWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf", "config.yaml")
	for _, body := range []string{"dry_run: true\n", "dry_run: false\nworkers: 2\n"} {
		if err := WriteFile(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Errorf("the file holds %q, %v; want %q", got, err, body)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Errorf("the folder holds %v, %v; want only the config file", entries, err)
	}

	// Where it cannot be written, the error says so and nothing is left.
	blocked := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(blocked, "config.yaml"), []byte("x")); err == nil {
		t.Error("WriteFile under a file succeeded")
	}
}

func TestCheck(t *testing.T) {
	p := fixtures.OSPath
	ok := Config{Source: p(`D:\In`), Destination: p(`D:\Music`)}
	// The library folder written another way: with a separator at its end,
	// and on Windows, where case does not matter, in other letters as well.
	library := p(`D:\Music\`)
	if runtime.GOOS == "windows" {
		library = `d:\MUSIC\`
	}
	with := func(change func(*Config)) Config {
		c := ok
		change(&c)
		return c
	}
	tests := []struct {
		name string
		cfg  Config
		want map[string]string // key to part of its problem
	}{
		{"nothing set", Config{}, nil},
		{"the usual", ok, nil},
		{"reorganizing in place", with(func(c *Config) { c.Source = library }), nil},
		{"folders of its own", with(func(c *Config) { c.Untagged, c.Other, c.Duplicates = p(`E:\U`), p(`E:\O`), p(`E:\D`) }), nil},
		{"workers below zero", with(func(c *Config) { c.Workers = -1 }), map[string]string{"workers": "0 or more"}},
		{"a contact that is an address", with(func(c *Config) { c.MusicBrainzContact = " me@example.org " }), nil},
		{"a contact that is a URL", with(func(c *Config) { c.MusicBrainzContact = "https://example.org/me" }), nil},
		{"a contact that is neither", with(func(c *Config) { c.MusicBrainzContact = "me" }), map[string]string{"musicbrainz_contact": "email address or a URL"}},
		{"a contact with a space", with(func(c *Config) { c.MusicBrainzContact = "me at example.org" }), map[string]string{"musicbrainz_contact": "without spaces"}},
		{"a contact without a host", with(func(c *Config) { c.MusicBrainzContact = "me@example" }), map[string]string{"musicbrainz_contact": "email address"}},
		{"a compilations name with a slash", with(func(c *Config) { c.Compilations = "Various/Artists" }), map[string]string{"compilations": "cannot hold"}},
		{"a compilations name ending in a full stop", with(func(c *Config) { c.Compilations = "Various." }), map[string]string{"compilations": "full stop"}},
		{"the library inside the source", with(func(c *Config) { c.Destination = p(`D:\In\Music`) }), map[string]string{"destination": "inside the source folder"}},
		{"the source inside the library", with(func(c *Config) { c.Source = p(`D:\Music\New`) }), nil},
		{"a folder with the source's name as its start", with(func(c *Config) { c.Destination = p(`D:\Inside`) }), nil},
		{"untagged is the library", with(func(c *Config) { c.Untagged = library }), map[string]string{"untagged": "library folder itself"}},
		{"duplicates is the source", with(func(c *Config) { c.Duplicates = p(`D:\In`) }), map[string]string{"duplicates": "source folder itself"}},
		{"other is where untagged goes by default", with(func(c *Config) { c.Other = p(`D:\Music\_Untagged`) }), map[string]string{"other": "same folder as untagged"}},
		{"two wrong at once", with(func(c *Config) { c.Workers, c.Compilations = -2, "A:B" }), map[string]string{"workers": "0 or more", "compilations": "cannot hold"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Check(tt.cfg)
			if len(got) != len(tt.want) {
				t.Errorf("Check = %v, want problems with %v", got, tt.want)
			}
			for key, part := range tt.want {
				if !strings.Contains(got[key], part) {
					t.Errorf("Check[%q] = %q, want it to say %q", key, got[key], part)
				}
			}
		})
	}
}
