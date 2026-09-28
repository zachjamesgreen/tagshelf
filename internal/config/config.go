// Package config loads tagshelf's settings from a YAML file and the command
// line.
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"

	"tagshelf/internal/rules"
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

// Reorganizing reports whether this run re-sorts an existing library in place,
// which is what a source equal to the destination means.
func (c Config) Reorganizing() bool {
	return c.Source != "" && rules.PathKey(c.Source) == rules.PathKey(c.Destination)
}

// WithDefaults fills in the values a run needs but the caller may have left at
// zero. It is idempotent, so both main and RunOrganizer can apply it.
func (c Config) WithDefaults() Config {
	if c.Untagged == "" && c.Destination != "" {
		c.Untagged = filepath.Join(c.Destination, "_Untagged")
	}
	if c.Other == "" && c.Destination != "" {
		c.Other = filepath.Join(c.Destination, "_Other")
	}
	if c.Compilations == "" {
		c.Compilations = rules.DefaultCompilations
	}
	if c.Workers <= 0 {
		c.Workers = runtime.NumCPU()
	}
	if c.Workers < 1 {
		c.Workers = 1
	}
	return c
}

// Overrides carries only the settings the user supplied on the command line.
// DryRun and Verbose are pointers so that "not given" stays distinguishable from
// an explicit false.
type Overrides struct {
	Source       string
	Destination  string
	Untagged     string
	Other        string
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
// the working directory is advisory and is ignored (with a warning) if it turns
// out to belong to some other program.
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
			if err := yaml.Unmarshal(data, &cfg); err != nil {
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
