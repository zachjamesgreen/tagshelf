package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
)

// writeConfig writes a config file named name into a new temp folder and
// returns its path.
func writeConfig(t *testing.T, name, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureLog returns what Load logs for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &logs
}

// everyKey is a config file that sets every key to a value that is not its
// default, and wantEveryKey the Config it must load as.
const everyKey = `
source: "C:\\MusicSource"
destination: "C:\\MusicDest"
untagged: "C:\\MusicUntagged"
other: "C:\\MusicOther"
duplicates: "C:\\MusicDuplicates"
playlists: "C:\\MusicPlaylists"
auto_resolve_exact: false
compilations: "Compilations"
workers: 4
dry_run: true
verbose: true
cue_rewrite: false
playlist_rewrite: false
musicbrainz_contact: "me@example.com"
genre_from_musicbrainz: replace
cover_art_file: true
cover_art_embed: true
cover_art_replace: smaller
database: "C:\\Data\\music.db"
`

func wantEveryKey() Config {
	off := false
	return Config{
		Source: `C:\MusicSource`, Destination: `C:\MusicDest`, Untagged: `C:\MusicUntagged`, Other: `C:\MusicOther`,
		Duplicates: `C:\MusicDuplicates`, Playlists: `C:\MusicPlaylists`, PlaylistRewrite: &off, AutoResolveExact: &off, Compilations: "Compilations", Workers: 4,
		DryRun: true, Verbose: true, CueRewrite: &off, MusicBrainzContact: "me@example.com", GenreFromMusicBrainz: "replace", CoverArtFile: true, CoverArtEmbed: true, CoverArtReplace: "smaller", Database: `C:\Data\music.db`,
	}
}

// TestLoadConfigEveryKey checks each key of a config file reaches its field,
// and that the file names every key there is.
func TestLoadConfigEveryKey(t *testing.T) {
	cfg, err := Load(writeConfig(t, "all.yaml", everyKey), Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if want := wantEveryKey(); !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load = %+v\nwant   %+v", cfg, want)
	}
	if cfg.RewriteCues() || cfg.AutoResolvesExact() || cfg.RewritePlaylists() {
		t.Errorf("cue_rewrite, auto_resolve_exact and playlist_rewrite are off in the file, got %t, %t and %t",
			cfg.RewriteCues(), cfg.AutoResolvesExact(), cfg.RewritePlaylists())
	}
	for _, key := range knownKeys() {
		if !strings.Contains(everyKey, "\n"+key+":") {
			t.Errorf("the test's config file lacks the key %q", key)
		}
	}
	v := reflect.ValueOf(cfg)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("field %s was left at its zero value", v.Type().Field(i).Name)
		}
	}
}

// TestLoadConfigEveryOverride checks each override replaces the file's value.
func TestLoadConfigEveryOverride(t *testing.T) {
	on, off := true, false
	ov := Overrides{Source: "s", Destination: "d", Untagged: "u", Other: "o", Duplicates: "dup", Playlists: "lists", Compilations: "VA",
		Workers: 16, DryRun: &off, Verbose: &off, CueRewrite: &on, PlaylistRewrite: &on, Database: "db"}
	cfg, err := Load(writeConfig(t, "all.yaml", everyKey), ov)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := wantEveryKey()
	want.Source, want.Destination, want.Untagged, want.Other, want.Duplicates = "s", "d", "u", "o", "dup"
	want.Compilations, want.Workers, want.DryRun, want.Verbose, want.CueRewrite, want.Database = "VA", 16, false, false, &on, "db"
	want.Playlists, want.PlaylistRewrite = "lists", &on
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load = %+v\nwant   %+v", cfg, want)
	}
	if !cfg.RewriteCues() || !cfg.RewritePlaylists() {
		t.Error("the cue and playlist rewrite overrides did not turn rewriting back on")
	}
	v := reflect.ValueOf(ov)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("the test leaves override %s unset", v.Type().Field(i).Name)
		}
	}
}

func TestLoadConfigCueRewriteDefaultsOn(t *testing.T) {
	cfg, err := Load(writeConfig(t, "empty.yaml", ""), Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.RewriteCues() {
		t.Error("cue_rewrite must default to on")
	}
	if !cfg.RewritePlaylists() {
		t.Error("playlist_rewrite must default to on")
	}
	off := false
	if cfg, err = Load(writeConfig(t, "empty.yaml", ""), Overrides{CueRewrite: &off}); err != nil || cfg.RewriteCues() {
		t.Errorf("Load with cue rewriting overridden off = %+v, %v", cfg, err)
	}
}

// TestLoadConfigFilesWithoutSettings checks the files that hold no settings
// load as an empty configuration.
func TestLoadConfigFilesWithoutSettings(t *testing.T) {
	for _, yaml := range []string{"", "\n", "# only a comment\n", "---\n", "{}\n"} {
		cfg, err := Load(writeConfig(t, "none.yaml", yaml), Overrides{})
		if err != nil || !reflect.DeepEqual(cfg, Config{}) {
			t.Errorf("Load of %q = %+v, %v; want an empty config", yaml, cfg, err)
		}
	}
}

// TestLoadConfigUnknownKeyIsFatal checks a key tagshelf does not know stops
// the run: a misspelt dry_run would otherwise make a real run of a preview.
func TestLoadConfigUnknownKeyIsFatal(t *testing.T) {
	for _, key := range []string{"dry-run", "dryrun", "DRY_RUN", "destinaton", "<<"} {
		yaml := "destination: \"C:\\\\Dst\"\n" + key + ": true\n"

		_, err := Load(writeConfig(t, "named.yaml", yaml), Overrides{})
		if err == nil || !strings.Contains(err.Error(), `unknown key "`+key+`" on line 2`) {
			t.Errorf("Load of a named file with %q: error = %v, want one naming the key and its line", key, err)
		}
		if err != nil && !strings.Contains(err.Error(), "dry_run") {
			t.Errorf("error %q does not list the keys there are", err)
		}

		// A config.yaml found in the working directory is held to the same.
		t.Chdir(filepath.Dir(writeConfig(t, DefaultConfigName, yaml)))
		logs := captureLog(t)
		cfg, err := Load("", Overrides{Source: "C:\\Src"})
		if err == nil || !strings.Contains(err.Error(), `unknown key "`+key+`"`) {
			t.Errorf("Load of a discovered file with %q: error = %v, want one naming the key (log %q)", key, err, logs)
		}
		if !reflect.DeepEqual(cfg, Config{}) {
			t.Errorf("Load of a discovered file with %q returned %+v with its error", key, cfg)
		}
	}
}

// TestLoadConfigDiscoveredFile checks a config.yaml in the working directory,
// which is how tagshelf is normally run, is used and said to be.
func TestLoadConfigDiscoveredFile(t *testing.T) {
	path := writeConfig(t, DefaultConfigName, everyKey)
	t.Chdir(filepath.Dir(path))
	logs := captureLog(t)

	cfg, err := Load("", Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if want := wantEveryKey(); !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load = %+v\nwant   %+v", cfg, want)
	}
	if !strings.Contains(logs.String(), "Using config file discovered in working directory") || !strings.Contains(logs.String(), DefaultConfigName) {
		t.Errorf("log %q does not say which file was used", logs)
	}

	// A file named with -config is used instead, and the found one not read.
	cfg, err = Load(writeConfig(t, "named.yaml", "workers: 2\n"), Overrides{})
	if err != nil || !reflect.DeepEqual(cfg, Config{Workers: 2}) {
		t.Errorf("Load of a named file beside a config.yaml = %+v, %v; want only the named file's settings", cfg, err)
	}
}

// TestLoadConfigWithoutAnyFile checks the flags stand alone where there is no
// config.yaml.
func TestLoadConfigWithoutAnyFile(t *testing.T) {
	t.Chdir(t.TempDir())
	logs := captureLog(t)
	cfg, err := Load("", Overrides{Source: "s", Destination: "d"})
	if err != nil || !reflect.DeepEqual(cfg, Config{Source: "s", Destination: "d"}) {
		t.Errorf("Load = %+v, %v; want only the overrides", cfg, err)
	}
	if logs.Len() != 0 {
		t.Errorf("Load logged %q with no config file about", logs)
	}
}

func TestLoadConfigVerbose(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "verbose_config.yaml")
	yamlContent := `
source: "C:\\MusicSource"
destination: "C:\\MusicDest"
verbose: true
dry_run: true
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.Verbose || !cfg.DryRun {
		t.Errorf("Expected verbose and dry_run true from YAML, got %+v", cfg)
	}

	// An explicit -verbose=false / -dry-run=false must be able to turn the
	// config file's setting back off.
	off := false
	cfgOff, err := Load(configPath, Overrides{Verbose: &off, DryRun: &off})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfgOff.Verbose {
		t.Errorf("Expected -verbose=false to override verbose: true from YAML")
	}
	if cfgOff.DryRun {
		t.Errorf("Expected -dry-run=false to override dry_run: true from YAML")
	}

	on := true
	cfgOn, err := Load(configPath, Overrides{Verbose: &on})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfgOn.Verbose {
		t.Errorf("Expected verbose=true with CLI flag")
	}
}

func TestLoadConfigMissingExplicitFileIsFatal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does_not_exist.yaml")
	if _, err := Load(missing, Overrides{Source: "C:\\Src", Destination: "C:\\Dst"}); err == nil {
		t.Errorf("Expected an error for an explicitly requested config file that cannot be read")
	}
}

func TestLoadConfigMalformedExplicitFileIsFatal(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(configPath, []byte("workers: not-a-number\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath, Overrides{}); err == nil {
		t.Errorf("Expected an error for an explicitly requested config file that cannot be parsed")
	}
}

func TestLoadConfigForeignDiscoveredFileIsIgnored(t *testing.T) {
	// An unrelated config.yaml belonging to some other program. The keys
	// before and after the one that cannot be read must not leak into the run.
	foreign := "destination: \"X:\\\\FromForeignFile\"\ndry_run: true\nworkers: [1, 2, 3]\nverbose: true\n"
	t.Chdir(filepath.Dir(writeConfig(t, DefaultConfigName, foreign)))
	logs := captureLog(t)

	cfg, err := Load("", Overrides{Source: "C:\\Src"})
	if err != nil {
		t.Fatalf("A foreign config.yaml must not abort a flag-driven run: %v", err)
	}
	if want := (Config{Source: "C:\\Src"}); !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load = %+v, want only the CLI flags: %+v", cfg, want)
	}
	if !strings.Contains(logs.String(), "Warning: ignoring the config.yaml in the working directory: ") {
		t.Errorf("log %q does not warn that the file was ignored", logs)
	}

	// Nor does a file that is not YAML at all, or not a mapping.
	for _, yaml := range []string{"destination: [unclosed\n", "- a\n- list\n", "just words\n"} {
		t.Chdir(filepath.Dir(writeConfig(t, DefaultConfigName, yaml)))
		cfg, err := Load("", Overrides{Source: "C:\\Src"})
		if err != nil || !reflect.DeepEqual(cfg, Config{Source: "C:\\Src"}) {
			t.Errorf("Load beside a config.yaml of %q = %+v, %v; want it ignored", yaml, cfg, err)
		}
		// Named with -config, the same file is an error.
		if _, err := Load(DefaultConfigName, Overrides{}); err == nil {
			t.Errorf("Load of a named file of %q succeeded", yaml)
		}
	}
}

func TestWithDefaults(t *testing.T) {
	cfg := Config{Destination: filepath.Join("C:", "Out")}.WithDefaults()
	if cfg.Workers < 1 {
		t.Errorf("Expected WithDefaults to pick at least one worker, got %d", cfg.Workers)
	}
	expectedUntagged := filepath.Join("C:", "Out", "_Untagged")
	if cfg.Untagged != expectedUntagged {
		t.Errorf("Expected untagged %q, got %q", expectedUntagged, cfg.Untagged)
	}
	expectedOther := filepath.Join("C:", "Out", "_Other")
	if cfg.Other != expectedOther {
		t.Errorf("Expected other %q, got %q", expectedOther, cfg.Other)
	}
	if cfg.Compilations != "Various Artists" {
		t.Errorf("Expected compilations to default to Various Artists, got %q", cfg.Compilations)
	}

	explicit := Config{Destination: "d", Untagged: "u", Other: "o", Compilations: "c", Workers: 3}.WithDefaults()
	if explicit.Untagged != "u" || explicit.Other != "o" || explicit.Compilations != "c" || explicit.Workers != 3 {
		t.Errorf("WithDefaults must not overwrite explicit values: %+v", explicit)
	}
}

// TestDefaultsAreTheEngines checks that the settings the CLI prints are the
// ones a run uses: the defaults are worked out in one place, the engine's
// options, and a Config filled in from them is not changed by a second pass.
func TestDefaultsAreTheEngines(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{Destination: filepath.Join("lib", "Music")},
		{Destination: "d", Untagged: "u", Workers: -4},
		{Destination: "d", Other: "o", Duplicates: "x", Compilations: "c", Workers: 3},
	} {
		got := cfg.WithDefaults()
		o := engine.Options{Destination: cfg.Destination, Untagged: cfg.Untagged, Other: cfg.Other, Duplicates: cfg.Duplicates,
			Compilations: cfg.Compilations, Workers: cfg.Workers}.WithDefaults()
		if got.Untagged != o.Untagged || got.Other != o.Other || got.Duplicates != o.Duplicates || got.Compilations != o.Compilations || got.Workers != o.Workers {
			t.Errorf("Config%+v.WithDefaults() = %+v, want the engine's %s, %s, %s, %s, %d", cfg, got, o.Untagged, o.Other, o.Duplicates, o.Compilations, o.Workers)
		}
		if again := got.WithDefaults(); !reflect.DeepEqual(again, got) {
			t.Errorf("a second WithDefaults changed %+v to %+v", got, again)
		}
		if got.Workers < 1 {
			t.Errorf("Config%+v.WithDefaults() has %d workers", cfg, got.Workers)
		}
	}
}

func TestLoadConfigMusicBrainzContact(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mb_config.yaml")
	if err := os.WriteFile(configPath, []byte("musicbrainz_contact: \"me@example.com\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath, Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.MusicBrainzContact != "me@example.com" {
		t.Errorf("MusicBrainzContact = %q, want me@example.com", cfg.MusicBrainzContact)
	}
}

func TestLoadConfigDuplicates(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "dup_config.yaml")
	if err := os.WriteFile(configPath, []byte("duplicates: \"C:\\\\Dups\"\nauto_resolve_exact: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath, Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Duplicates != `C:\Dups` || cfg.AutoResolvesExact() {
		t.Errorf("config = %+v, want duplicates C:\\Dups and auto-resolve off", cfg)
	}
	cfg, err = Load(configPath, Overrides{Duplicates: `D:\Dups`})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Duplicates != `D:\Dups` {
		t.Errorf("Expected -duplicates to override, got %q", cfg.Duplicates)
	}

	def := Config{Destination: filepath.Join("C:", "Out")}.WithDefaults()
	if want := filepath.Join("C:", "Out", "_Duplicates"); def.Duplicates != want {
		t.Errorf("default duplicates = %q, want %q", def.Duplicates, want)
	}
	if !def.AutoResolvesExact() {
		t.Error("auto_resolve_exact must default to on")
	}
	if explicit := (Config{Destination: "d", Duplicates: "x"}).WithDefaults(); explicit.Duplicates != "x" {
		t.Errorf("WithDefaults overwrote duplicates: %q", explicit.Duplicates)
	}
}

func TestLoadConfigDatabase(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "db_config.yaml")
	if err := os.WriteFile(configPath, []byte(`database: "C:\\Data\\music.db"`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Database != `C:\Data\music.db` {
		t.Errorf("Expected database from YAML, got %q", cfg.Database)
	}

	cfg, err = Load(configPath, Overrides{Database: `D:\other.db`})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Database != `D:\other.db` {
		t.Errorf("Expected -db to override the database, got %q", cfg.Database)
	}
}

// TestGenreMode checks what a MusicBrainz match does with the genre: fill
// unless the setting says otherwise, and a value that is none of the three
// is a problem to put right before the settings are saved.
func TestGenreMode(t *testing.T) {
	for value, want := range map[string]string{"": GenreFill, "fill": GenreFill, " Replace ": GenreReplace, "OFF": GenreOff, "always": "always"} {
		c := Config{GenreFromMusicBrainz: value}
		if got := c.GenreMode(); got != want {
			t.Errorf("GenreMode with %q = %q, want %q", value, got, want)
		}
		problem := Check(c)["genre_from_musicbrainz"]
		if bad := value == "always"; (problem != "") != bad {
			t.Errorf("with %q the problem is %q", value, problem)
		}
	}
}
