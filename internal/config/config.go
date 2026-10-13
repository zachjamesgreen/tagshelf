// Package config loads tagshelf's settings from a YAML file and the command
// line.
package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
)

// DefaultConfigName is the config file picked up from the working directory
// when -config is not given.
const DefaultConfigName = "config.yaml"

// Config is everything a run needs, merged from the config file and flags.
type Config struct {
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
	Untagged    string `yaml:"untagged"`
	Other       string `yaml:"other"`
	// Duplicates is where the losing copies of duplicates go.
	Duplicates string `yaml:"duplicates"`
	// AutoResolveExact lets runs resolve exact duplicate groups they added a
	// file to. Nil means the default, which is on.
	AutoResolveExact *bool `yaml:"auto_resolve_exact"`
	// Compilations is the artist folder for Various Artists albums.
	Compilations string `yaml:"compilations"`
	Workers      int    `yaml:"workers"`
	DryRun       bool   `yaml:"dry_run"`
	Verbose      bool   `yaml:"verbose"`
	// CueRewrite fixes the FILE lines of moved cue sheets. Nil means the
	// default, which is on.
	CueRewrite *bool `yaml:"cue_rewrite"`
	// MusicBrainzContact is the email address or URL sent in the MusicBrainz
	// User-Agent. MusicBrainz lookups are disabled while it is empty.
	MusicBrainzContact string `yaml:"musicbrainz_contact"`
	// GenreFromMusicBrainz says what a MusicBrainz match does with the
	// genre: GenreOff, GenreFill or GenreReplace. Empty means the default,
	// GenreFill.
	GenreFromMusicBrainz string `yaml:"genre_from_musicbrainz"`
	// CoverArtFile has a cover file written into an album's folder that has
	// none, when cover art is seen to: after a MusicBrainz match, by the
	// TUI's action on an album, and by tagshelf art.
	CoverArtFile bool `yaml:"cover_art_file"`
	// CoverArtReplace is when a cover an album already has is replaced by
	// the Cover Art Archive's: engine.CoverNever, CoverSmaller or
	// CoverAlways. Empty means the default, CoverNever.
	CoverArtReplace string `yaml:"cover_art_replace"`

	// Database is the library's database file. Empty means the default
	// location for the library's ID.
	Database string `yaml:"database"`
}

// What a MusicBrainz match does with a file's genre.
const (
	GenreOff     = "off"     // nothing: the genre is left as it is
	GenreFill    = "fill"    // writes MusicBrainz's genres to a file that has none
	GenreReplace = "replace" // writes them whenever MusicBrainz has any
)

// GenreMode is what a MusicBrainz match does with the genre: one of GenreOff,
// GenreFill and GenreReplace.
func (c Config) GenreMode() string {
	if mode := strings.ToLower(strings.TrimSpace(c.GenreFromMusicBrainz)); mode != "" {
		return mode
	}
	return GenreFill
}

// CoverReplace is when a cover an album has is replaced: one of
// engine.CoverNever, CoverSmaller and CoverAlways.
func (c Config) CoverReplace() string {
	if mode := strings.ToLower(strings.TrimSpace(c.CoverArtReplace)); mode != "" {
		return mode
	}
	return engine.CoverNever
}

// RewriteCues reports whether moved cue sheets get their FILE lines fixed.
func (c Config) RewriteCues() bool {
	return c.CueRewrite == nil || *c.CueRewrite
}

// AutoResolvesExact reports whether runs resolve exact duplicate groups on
// their own.
func (c Config) AutoResolvesExact() bool {
	return c.AutoResolveExact == nil || *c.AutoResolveExact
}

// Reorganizing reports whether this run re-sorts an existing library in place,
// which is what a source equal to the destination means.
func (c Config) Reorganizing() bool {
	return c.Source != "" && rules.PathKey(c.Source) == rules.PathKey(c.Destination)
}

// WithDefaults fills in the values a run needs but the caller may have left at
// zero: the untagged, other and duplicates folders, the compilations name and
// the worker count. The engine's options own those defaults, so the settings
// the CLI prints are the ones the run uses. It is idempotent.
func (c Config) WithDefaults() Config {
	o := engine.Options{
		Destination:  c.Destination,
		Untagged:     c.Untagged,
		Other:        c.Other,
		Duplicates:   c.Duplicates,
		Compilations: c.Compilations,
		Workers:      c.Workers,
	}.WithDefaults()
	c.Untagged, c.Other, c.Duplicates = o.Untagged, o.Other, o.Duplicates
	c.Compilations, c.Workers = o.Compilations, o.Workers
	return c
}

// unknownKeyError is a key in a config file that tagshelf has no setting for.
type unknownKeyError struct {
	key  string
	line int
}

func (e *unknownKeyError) Error() string {
	return fmt.Sprintf("an unknown key %q on line %d (the keys are %s)", e.key, e.line, strings.Join(knownKeys(), ", "))
}

// knownKeys lists the keys a config file may hold: Config's yaml names.
func knownKeys() []string {
	t := reflect.TypeFor[Config]()
	keys := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		keys = append(keys, name)
	}
	return keys
}

// parse reads the YAML of a config file into cfg. A key that is not one of
// knownKeys is an *unknownKeyError; anything else that keeps the file from
// being read as a Config is the YAML decoder's error, and may leave cfg
// half filled in.
func parse(data []byte, cfg *Config) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	// An empty file has no document; one that is not a mapping at the top is
	// left for Decode to refuse.
	if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		known := knownKeys()
		pairs := doc.Content[0].Content
		for i := 0; i+1 < len(pairs); i += 2 {
			if key := pairs[i]; !slices.Contains(known, key.Value) {
				return &unknownKeyError{key: key.Value, line: key.Line}
			}
		}
	}
	return doc.Decode(cfg)
}

// Overrides carries only the settings the user supplied on the command line.
// DryRun and Verbose are pointers so that "not given" stays distinguishable from
// an explicit false.
type Overrides struct {
	Source       string
	Destination  string
	Untagged     string
	Other        string
	Duplicates   string
	Compilations string
	Workers      int
	DryRun       *bool
	Verbose      *bool
	CueRewrite   *bool
	Database     string
}

// Find reports the config file Load reads when given configPath: that file,
// which is then explicit, or else a config.yaml in the working directory if
// one is there. path is empty when there is none.
// From says how a config file was found.
type From int

const (
	// FromNowhere is no config file at all.
	FromNowhere From = iota
	// FromFlag is the file named with -config.
	FromFlag
	// FromWorkingDir is a config.yaml in the working directory.
	FromWorkingDir
	// FromUser is the user's own config file, at UserPath.
	FromUser
)

// File is a config file as it was found and read.
type File struct {
	// Path is the file, empty when there is none.
	Path string
	From From
	// Config is what the file sets, zero when it could not be read.
	Config Config
	// Skipped says why a config.yaml in the working directory was passed
	// over, empty when none was.
	Skipped string
	// Text is the file as read, nil when it could not be.
	Text []byte
}

// Parse reads the text of a config file.
func Parse(data []byte) (Config, error) {
	var cfg Config
	err := parse(data, &cfg)
	return cfg, err
}

// userDir is the folder of the user's own settings. The tests put a temp
// folder in its place.
var userDir = os.UserConfigDir

// UserPath is the user's own config file: %APPDATA%\tagshelf\config.yaml on
// Windows, and elsewhere tagshelf/config.yaml under $XDG_CONFIG_HOME or
// ~/.config. It is read when no file is named and the working directory has none, so that
// tagshelf can be started from any folder, and it is the file the terminal UI
// writes when there is no other.
func UserPath() (string, error) {
	dir, err := userDir()
	if err != nil {
		return "", fmt.Errorf("finding the folder of your settings: %w", err)
	}
	return filepath.Join(dir, "tagshelf", DefaultConfigName), nil
}

// Read finds the config file and reads it. configPath is the path passed to
// -config; when it is empty, a config.yaml in the working directory is read
// if one is there, and else the file at UserPath if that is there.
//
// A config file the user asked for by name is mandatory: failing to read or
// parse it is an error, because its dry_run setting may be the only thing
// standing between the user and a destructive run. When it does not exist the
// error is an fs.ErrNotExist. The file at UserPath is tagshelf's own, and as
// mandatory once it exists.
//
// A file merely discovered in the working directory is advisory, and is
// passed over if it cannot be read or parsed, as when it belongs to some
// other program: File.Skipped then says why.
//
// A key tagshelf does not know is an error in any of them, for the same
// reason as above: a misspelt dry_run must not be dropped in silence.
//
// With an error, the File still names the file at fault.
func Read(configPath string) (File, error) {
	if configPath != "" {
		f := File{Path: configPath, From: FromFlag}
		err := f.read()
		return f, err
	}

	var skipped string
	if _, err := os.Stat(DefaultConfigName); err == nil {
		f := File{Path: DefaultConfigName, From: FromWorkingDir}
		if abs, err := filepath.Abs(f.Path); err == nil {
			f.Path = abs
		}
		err := f.read()
		var unknown *unknownKeyError
		if err == nil || errors.As(err, &unknown) {
			return f, err
		}
		skipped = err.Error()
	}

	user, err := UserPath()
	if err != nil {
		// Without a place for it there is no such file to read.
		return File{Skipped: skipped}, nil
	}
	if _, err := os.Stat(user); err != nil {
		return File{Skipped: skipped}, nil
	}
	f := File{Path: user, From: FromUser, Skipped: skipped}
	err = f.read()
	return f, err
}

// read reads the file at f.Path into f.Config.
func (f *File) read() error {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return fmt.Errorf("the config file %s cannot be read: %w", f.Path, err)
	}
	var cfg Config
	err = parse(data, &cfg)
	var unknown *unknownKeyError
	switch {
	case errors.As(err, &unknown):
		return fmt.Errorf("the config file %s has %w", f.Path, unknown)
	case err != nil:
		return fmt.Errorf("the config file %s is not valid YAML: %w", f.Path, err)
	}
	f.Config, f.Text = cfg, data
	return nil
}

// Load reads the config file Read finds for configPath and merges it with
// the CLI overrides. It logs which file it used when the user did not name
// one, and why it passed over a config.yaml in the working directory.
func Load(configPath string, ov Overrides) (Config, error) {
	cfg, _, err := LoadFile(configPath, ov)
	return cfg, err
}

// LoadFile is Load that also returns the file it read, for a message that
// has to name it.
func LoadFile(configPath string, ov Overrides) (Config, File, error) {
	f, err := Read(configPath)
	if f.Skipped != "" {
		log.Printf("Warning: ignoring the %s in the working directory: %s\n", DefaultConfigName, f.Skipped)
	}
	if err != nil {
		return Config{}, f, err
	}
	switch f.From {
	case FromWorkingDir:
		log.Printf("Using config file discovered in working directory: %s\n", f.Path)
	case FromUser:
		log.Printf("Using your config file: %s\n", f.Path)
	}
	return f.Config.With(ov), f, nil
}

// With is the config with the settings typed on the command line in place of
// its own.
func (c Config) With(ov Overrides) Config {
	cfg := c

	// CLI flags override config file
	if ov.Source != "" {
		cfg.Source = ov.Source
	}
	if ov.Destination != "" {
		cfg.Destination = ov.Destination
	}
	if ov.Untagged != "" {
		cfg.Untagged = ov.Untagged
	}
	if ov.Other != "" {
		cfg.Other = ov.Other
	}
	if ov.Duplicates != "" {
		cfg.Duplicates = ov.Duplicates
	}
	if ov.Compilations != "" {
		cfg.Compilations = ov.Compilations
	}
	if ov.Workers > 0 {
		cfg.Workers = ov.Workers
	}
	if ov.DryRun != nil {
		cfg.DryRun = *ov.DryRun
	}
	if ov.Verbose != nil {
		cfg.Verbose = *ov.Verbose
	}
	if ov.CueRewrite != nil {
		cfg.CueRewrite = ov.CueRewrite
	}
	if ov.Database != "" {
		cfg.Database = ov.Database
	}

	return cfg
}
