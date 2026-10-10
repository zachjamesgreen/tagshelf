package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/tui"
)

// optionalKeys are the settings a new config file leaves commented out while
// they are empty.
var optionalKeys = map[string]bool{"untagged": true, "other": true, "duplicates": true, "musicbrainz_contact": true, "database": true}

// pathKeys are the settings that name a folder or a file.
var pathKeys = []string{"source", "destination", "untagged", "other", "duplicates", "database"}

// newFileConfig is what the Settings tab shows before there is a config
// file: the template's settings, without its made-up folders. Dry run is on
// in it, so that the first run of a new set-up is a preview.
func newFileConfig() config.Config {
	var cfg config.Config
	f, err := config.Parse(configTemplate)
	if err == nil {
		cfg = f
	}
	cfg.Source, cfg.Destination = "", ""
	return cfg
}

// Load reads the config file for the Settings tab: the one tagshelf tui
// read, the one it was named, or else the user's own, which need not exist.
func (s *tuiSession) Load() tui.SettingsFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := tui.SettingsFile{Session: s.flagValues(), DataFolder: dataFolderName()}
	f, err := config.Read(*s.flags.config)
	out.Path = f.Path
	if f.From == config.FromNowhere {
		user, perr := config.UserPath()
		if perr != nil {
			out.Problem = fmt.Sprintf("there is nowhere to keep a config file (%v); name one with -config", perr)
			return out
		}
		out.Path = user
	}
	switch {
	case f.From == config.FromNowhere, f.From == config.FromFlag && errors.Is(err, fs.ErrNotExist):
		out.New, out.Config = true, newFileConfig()
	case err != nil:
		out.Problem = err.Error()
	default:
		out.Config, out.Text = f.Config, f.Text
		out.Problems = config.PathProblems(f.Config)
	}
	return out
}

// flagValues are the settings typed as flags, which this session goes by in
// place of the config file's, as the Settings tab shows them.
func (s *tuiSession) flagValues() map[string]string {
	keys := map[string]string{"source": "source", "dest": "destination", "untagged": "untagged", "other": "other",
		"duplicates": "duplicates", "compilations": "compilations", "workers": "workers", "db": "database"}
	onOff := map[bool]string{true: "on", false: "off"}
	out := map[string]string{}
	s.flags.set.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "dry-run":
			out["dry_run"] = onOff[*s.flags.dryRun]
		case "no-cue-rewrite":
			out["cue_rewrite"] = onOff[!*s.flags.noCueRewrite]
		default:
			if key, ok := keys[f.Name]; ok && f.Value.String() != "" && f.Value.String() != "0" {
				out[key] = f.Value.String()
				if key != "compilations" && key != "workers" {
					out[key] = filepath.Clean(f.Value.String())
				}
			}
		}
	})
	return out
}

// Check looks at settings about to be saved: what is wrong with them, and
// what the user should say yes to first.
func (s *tuiSession) Check(c config.Config, read tui.SettingsFile) tui.SettingsCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	check := tui.SettingsCheck{Problems: config.Check(c)}
	values := c.Values()
	for _, key := range pathKeys {
		// A path that is not whole would mean another folder wherever
		// tagshelf is started from. One from ~ is whole.
		path := values[key].(string)
		if path != "" && !config.StartsAtHome(path) && !filepath.IsAbs(path) && check.Problems[key] == "" {
			check.Problems[key] = here(`must be a whole path, such as D:\Music`)
		}
	}
	// The folders as a run would use them: ~ in full, and through a link.
	source, dest := usablePath(c.Source), usablePath(c.Destination)
	if c.Source != "" && check.Problems["source"] == "" {
		switch info, err := os.Stat(source); {
		case errors.Is(err, fs.ErrNotExist):
			check.Problems["source"] = "does not exist"
		case err != nil:
			check.Problems["source"] = "cannot be read: " + err.Error()
		case !info.IsDir():
			check.Problems["source"] = "is a file, not a folder"
		}
	}
	if c.Destination != "" && check.Problems["destination"] == "" {
		switch info, err := os.Stat(dest); {
		case errors.Is(err, fs.ErrNotExist):
			check.Confirm = append(check.Confirm, "Make the library folder "+dest+"?")
		case err != nil:
			check.Problems["destination"] = "cannot be read: " + err.Error()
		case !info.IsDir():
			check.Problems["destination"] = "is a file, not a folder"
		}
	}
	if len(check.Problems) > 0 {
		check.Confirm = nil
		return check
	}

	// What saving does to the library this session shows. A flag that names
	// the folder or the database keeps the session on it.
	if s.st == nil {
		return check
	}
	_, destFlag := read.Session["destination"]
	_, dbFlag := read.Session["database"]
	switch {
	case !destFlag && c.Destination != "" && rules.PathKey(dest) != rules.PathKey(s.cfg.Destination):
		check.Confirm = append(check.Confirm, "This opens a different library. Your music in "+s.cfg.Destination+" is not moved.")
	case !destFlag && c.Destination == "":
		check.Confirm = append(check.Confirm, "This leaves tagshelf without a library folder. Your music in "+s.cfg.Destination+" is not touched.")
	case !dbFlag && usablePath(c.Database) != usablePath(read.Config.Database):
		check.Confirm = append(check.Confirm, "This uses a different database for the library. Its run history stays in the old one.")
	}
	return check
}

// Save writes the settings to the config file that was read, unless the file
// has changed since. Only the lines of the settings that changed are written
// again; a file that is not there yet is made from the template.
func (s *tuiSession) Save(c config.Config, read tui.SettingsFile) (note string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := os.ReadFile(read.Path)
	switch {
	case read.New && !errors.Is(err, fs.ErrNotExist):
		return "", tui.ErrSettingsChanged
	case !read.New && (err != nil || !bytes.Equal(current, read.Text)):
		return "", tui.ErrSettingsChanged
	}

	set := map[string]any{}
	base := current
	changed := c.Changed(read.Config)
	if read.New {
		base = configTemplate
		for key, value := range c.Values() {
			if optionalKeys[key] && value == "" {
				continue
			}
			set[key] = value
		}
	} else {
		values := c.Values()
		for _, key := range changed {
			set[key] = values[key]
		}
	}
	text, err := config.Update(base, set)
	if err != nil {
		return "", err
	}
	if c.Destination != "" {
		if err := os.MkdirAll(usablePath(c.Destination), 0o755); err != nil {
			return "", fmt.Errorf("making the library folder: %w", err)
		}
	}
	if err := config.WriteFile(read.Path, text); err != nil {
		return "", err
	}

	if s.st != nil {
		for _, key := range changed {
			switch key {
			case "compilations", "untagged", "other", "duplicates":
				return "Files already in the library stay where they are: press r on Home to reorganize", nil
			}
		}
	}
	return "", nil
}
