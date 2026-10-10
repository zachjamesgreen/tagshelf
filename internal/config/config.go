// Package config loads tagshelf's settings from a YAML file and the command
// line.
package config

import (
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

	// Database is the library's database file. Empty means the default
	// location for the library's ID.
	Database string `yaml:"database"`
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
	return fmt.Sprintf("unknown key %q on line %d (the keys are %s)", e.key, e.line, strings.Join(knownKeys(), ", "))
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

// Load merges a YAML config file with the CLI overrides. configPath is the
// path passed to -config; when it is empty a config.yaml in the working
// directory is picked up if one happens to be there.
//
// A config file the user asked for by name is mandatory: failing to read or
// parse it is an error, because its dry_run setting may be the only thing
// standing between the user and a destructive run. A file merely discovered in
// the working directory is advisory and is ignored (with a warning) if it
// cannot be read or parsed, as when it belongs to some other program.
//
// A key tagshelf does not know is an error in either file, for the same
// reason: a misspelt dry_run must not be dropped in silence.
func Load(configPath string, ov Overrides) (Config, error) {
	var cfg Config

	explicit := configPath != ""
	if !explicit {
		if _, err := os.Stat(DefaultConfigName); err == nil {
			configPath = DefaultConfigName
		}
	}

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		switch {
		case err != nil && explicit:
			return Config{}, fmt.Errorf("failed to read config file %q: %w", configPath, err)
		case err != nil:
			log.Printf("Warning: ignoring unreadable config file %q: %v\n", configPath, err)
		default:
			err := parse(data, &cfg)
			if unknown, ok := err.(*unknownKeyError); ok {
				return Config{}, fmt.Errorf("config file %q: %w", configPath, unknown)
			}
			if err != nil {
				if explicit {
					return Config{}, fmt.Errorf("failed to parse config file %q: %w", configPath, err)
				}
				log.Printf("Warning: ignoring unparsable %s in working directory: %v\n", configPath, err)
				cfg = Config{}
			} else if !explicit {
				abs, absErr := filepath.Abs(configPath)
				if absErr != nil {
					abs = configPath
				}
				log.Printf("Using config file discovered in working directory: %s\n", abs)
			}
		}
	}

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

	return cfg, nil
}
