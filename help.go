package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// stdout is where help that was asked for goes. Help shown because of a
// mistake goes where the mistake is reported, which is log.Writer(). The tests
// put a buffer in its place.
var stdout io.Writer = os.Stdout

// helpWidth is the widest a line of help may be.
const helpWidth = 80

// flagGroup is the flags listed under one heading of a help screen.
type flagGroup struct {
	heading string
	flags   []string
}

// helpScreen is the help of one command: everything above its flags, the
// flags in groups, and the lines for what is not a flag of the set.
type helpScreen struct {
	intro  string
	groups []flagGroup
	// extra are lines listed after the last group, as "-name" and what it does.
	extra [][2]string
}

var organizeHelp = helpScreen{
	intro: `tagshelf sorts music files into a library by their tags:
Artist\Album (Year)\Track - Title. It never deletes or writes over a file.

Usage:
  tagshelf [flags]        organize the source folder into the library
  tagshelf tui [flags]    open the terminal UI on the library (tagshelf tui -h)
  tagshelf -version       print the version
  tagshelf -h             show this help

Examples:
  tagshelf -source D:\Incoming -dest D:\Music -dry-run
      Preview: say where every file would go, and move nothing.
  tagshelf -source D:\Incoming -dest D:\Music
      Organize D:\Incoming into the library at D:\Music.
  tagshelf -reorganize D:\Music
      Re-sort a library in place, after its tags were changed.
  tagshelf -config my.yaml
      Take the settings from a config file (see config.example.yaml).
  tagshelf tui -dest D:\Music
      Review untagged files and duplicates in the terminal UI.
`,
	groups: []flagGroup{
		{"Folders", []string{"source", "dest", "reorganize", "untagged", "other", "duplicates", "compilations", "db"}},
		{"The run", []string{"dry-run", "index", "rescan", "no-cue-rewrite", "workers", "verbose"}},
		{"Other", []string{"config"}},
	},
	extra: [][2]string{{"-version", "Print the version."}, {"-h", "Show this help."}},
}

var tuiHelp = helpScreen{
	intro: `tagshelf tui opens the terminal UI on a library: review untagged files and
duplicates, edit tags, start runs and undo them.

Usage:
  tagshelf tui [flags]

Examples:
  tagshelf tui -dest D:\Music
      Open the library at D:\Music.
  tagshelf tui -source D:\Incoming -dest D:\Music
      The same, with o organizing D:\Incoming into it.
  tagshelf tui -config my.yaml
      Take the settings from a config file.
`,
	groups: []flagGroup{
		{"Folders", []string{"source", "dest", "untagged", "other", "duplicates", "compilations", "db"}},
		{"Runs started from the terminal UI", []string{"dry-run", "no-cue-rewrite", "workers"}},
		{"Other", []string{"config"}},
	},
	extra: [][2]string{{"-h", "Show this help."}},
}

// write prints the screen to w with the flags of set, and says which config
// file a command given configPath as its -config would read.
func (h helpScreen) write(w io.Writer, set *flag.FlagSet, configPath string) {
	type line struct{ name, usage string }
	var groups [][]line
	width := 0
	add := func(lines []line) {
		for _, l := range lines {
			width = max(width, len(l.name))
		}
		groups = append(groups, lines)
	}
	for _, g := range h.groups {
		var lines []line
		for _, name := range g.flags {
			f := set.Lookup(name)
			if f == nil {
				continue
			}
			value, usage := flag.UnquoteUsage(f)
			l := line{name: "-" + f.Name, usage: usage}
			if value != "" {
				l.name += " <" + value + ">"
			}
			lines = append(lines, l)
		}
		add(lines)
	}

	fmt.Fprint(w, h.intro)
	for i, lines := range groups {
		if i == len(groups)-1 {
			for _, e := range h.extra {
				lines = append(lines, line{e[0], e[1]})
			}
		}
		fmt.Fprintf(w, "\n%s:\n", h.groups[i].heading)
		for _, l := range lines {
			indent := 2 + width + 2
			for j, part := range wrap(l.usage, helpWidth-indent) {
				name := ""
				if j == 0 {
					name = l.name
				}
				fmt.Fprintf(w, "  %-*s  %s\n", width, name, part)
			}
		}
	}

	fmt.Fprintf(w, "\nConfig file:\n  %s\n", configFileNote(configPath))
}

// configFileNote says which config file a command given configPath as its
// -config would read.
func configFileNote(configPath string) string {
	path, explicit := config.Find(configPath)
	switch {
	case path == "":
		return "None: no -config was given, and this folder has no " + config.DefaultConfigName + "."
	case explicit:
		return path + " (named with -config)"
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path + " (found in this folder)"
}

// wrap breaks text into lines of at most width characters, at spaces. A word
// longer than width gets a line of its own.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}

// cannotStart says why the command does not start and what to do about it,
// and returns the exit code for that. what is a phrase without a full stop,
// todo one or more whole sentences.
func cannotStart(what, todo string) int {
	fmt.Fprintf(log.Writer(), "tagshelf cannot start: %s.\n%s\n", strings.TrimSuffix(what, "."), todo)
	return exitCannotStart
}

// flagProblem puts what the flag package says about a command line it could
// not read in tagshelf's words.
func flagProblem(err error) string {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: "); ok {
		return "there is no flag " + name
	}
	if name, ok := strings.CutPrefix(msg, "flag needs an argument: "); ok {
		return "the flag " + name + " needs a value after it"
	}
	return msg
}

// configProblem is why a config file keeps the command from starting.
func configProblem(err error) int {
	return cannotStart(err.Error(), "Fix the file, or name another one with -config.")
}

// libraryProblem is why the library at dest, which could not be opened,
// keeps the command from starting.
func libraryProblem(err error, dest string) int {
	var locked *store.LockedError
	switch {
	case errors.As(err, &locked):
		what := "another tagshelf run is working on the library at " + dest
		if locked.Holder.PID != 0 {
			what += fmt.Sprintf(" (process %d, started %s)", locked.Holder.PID,
				locked.Holder.Started.Local().Format("2006-01-02 15:04:05"))
		}
		return cannotStart(what, "Wait for it to finish, then try again. If no run is going, the lock is "+locked.Path+".")
	case errors.Is(err, store.ErrNoDatabase):
		return cannotStart("the library at "+dest+" has no index yet",
			"Make one first: tagshelf -index -dest \""+dest+"\"")
	case errors.Is(err, store.ErrOtherLibrary):
		return cannotStart(err.Error(), "Leave -db and database: out to use the library's own database.")
	}
	return cannotStart(fmt.Sprintf("the library database cannot be opened: %v", err),
		"Nothing was touched. Another database can be named with -db.")
}
