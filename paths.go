package main

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zachjamesgreen/tagshelf/internal/config"
)

// pathNames are what the settings that name a folder or a file are called in
// a message.
var pathNames = map[string]string{
	"source": "source folder", "destination": "library folder", "untagged": "untagged folder",
	"other": "other folder", "duplicates": "duplicates folder", "database": "database file",
}

// here gives a text of the help or of a message, written with the example
// folders of Windows, the form it has on this system: anywhere else D:\Music
// is ~/Music and a backslash is a slash. It is for texts tagshelf makes up,
// never for a path someone gave: off Windows a backslash can be part of a
// name.
func here(text string) string {
	if runtime.GOOS == "windows" {
		return text
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, `D:\`, "~/"), `\`, "/")
}

// pathOrigin says where the folders and files of a configuration came from,
// so that a message about one can name the place to change it.
type pathOrigin struct {
	// flags maps a setting to the flag it was typed as, for those that were.
	flags map[string]string
	// file is the config file the others are in, empty when there is none.
	file string
}

// typedPaths maps each folder and file setting typed on the command line to
// its flag.
func (f *libraryFlags) typedPaths() map[string]string {
	settings := map[string][]string{
		"source": {"source"}, "dest": {"destination"}, "reorganize": {"source", "destination"},
		"untagged": {"untagged"}, "other": {"other"}, "duplicates": {"duplicates"}, "db": {"database"},
	}
	typed := map[string]string{}
	f.set.Visit(func(fl *flag.Flag) {
		if fl.Value.String() == "" {
			return
		}
		for _, key := range settings[fl.Name] {
			typed[key] = "-" + fl.Name
		}
	})
	return typed
}

// pathProblem is a folder or a file setting that cannot be used as written.
type pathProblem struct {
	key, path string
	// flag is the flag it was given with, empty when it is the config
	// file's, and file that config file.
	flag, file string
	why, todo  string
}

func (p *pathProblem) Error() string { return p.what() }

// what says which path it is, where it was given, and what is wrong with it.
func (p *pathProblem) what() string {
	from := ""
	switch {
	case p.flag != "":
		from = ", given with " + p.flag + ","
	case p.file != "":
		from = ", set as " + p.key + " in the config file " + p.file + ","
	}
	return "the " + pathNames[p.key] + " " + p.path + from + " " + p.why
}

func (p *pathProblem) refusal() refusal { return refusal{p.what(), p.todo} }

// usablePaths gives cfg with its folders and its database file as this
// system uses them, and the defaults filled in. Each path that is set has ~
// put in full and is made absolute, so that it means the same wherever
// tagshelf is started from. A folder that is itself a symbolic link, as
// ~/Music is on many a Linux system, becomes the folder it leads to: a walk
// does not go through a link, so the run would otherwise find nothing.
// links says which folders those were.
//
// A path this system cannot use at all is reported as the problem: a Windows
// path anywhere but on Windows, or one that starts at another user's home.
func usablePaths(cfg config.Config, from pathOrigin) (out config.Config, links []string, problem *pathProblem) {
	values := cfg.Values()
	for _, key := range config.PathKeys() {
		path, _ := values[key].(string)
		if path == "" {
			continue
		}
		bad := &pathProblem{key: key, path: path, flag: from.flags[key]}
		if bad.flag == "" {
			bad.file = from.file
		}
		if config.ForeignPath(path) {
			bad.why, bad.todo = "is a Windows path", here(`Give the folder as this system writes it, as in D:\Music.`)
			return cfg, nil, bad
		}
		expanded, err := config.ExpandHome(path)
		switch {
		case errors.Is(err, config.ErrOtherHome):
			bad.why, bad.todo = "starts with another user's home folder", "tagshelf knows only ~ alone, your own home folder. Write the folder out in full."
			return cfg, nil, bad
		case err != nil:
			bad.why, bad.todo = "cannot be found: "+err.Error(), "Write the folder out in full, without ~."
			return cfg, nil, bad
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			bad.why, bad.todo = "is not a path this system can use: "+err.Error(), "Check the path."
			return cfg, nil, bad
		}
		if key != "database" {
			if real, ok := linkTarget(abs); ok {
				links = append(links, "The "+pathNames[key]+" "+abs+" is a link to "+real+".")
				abs = real
			}
		}
		cfg, _ = cfg.Set(key, abs)
	}
	return cfg.WithDefaults(), links, nil
}

// linkTarget is the folder a symbolic link at path leads to, and whether path
// is such a link. A link that leads nowhere is left as it is, for whatever
// opens it to report.
func linkTarget(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return path, false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path, false
	}
	return real, true
}

// usablePath gives one folder as this system uses it, for a check of a
// setting that is not saved yet: ~ in full, absolute, and through a link. A
// path it cannot make sense of is returned as it is.
func usablePath(path string) string {
	expanded, err := config.ExpandHome(path)
	if err != nil || path == "" || config.ForeignPath(path) {
		return path
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return expanded
	}
	real, _ := linkTarget(abs)
	return real
}
