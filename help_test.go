package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/tui"
)

// commandFlags are the flag sets of the two commands, each with its help.
func commandFlags() map[string]*libraryFlags {
	tuiSet := flag.NewFlagSet("tagshelf tui", flag.ContinueOnError)
	return map[string]*libraryFlags{
		"tagshelf": organizeFlags(flag.NewFlagSet("tagshelf", flag.ContinueOnError)),
		"tagshelf tui": addLibraryFlags(tuiSet, tuiHelp, "The source `folder`.",
			"The library `folder`.", "Dry run."),
	}
}

// TestHelpListsEveryFlagOnce checks that a help screen lists each flag of its
// command under exactly one heading, and no flag the command does not take:
// the lists are written by hand, so a new flag is easily left out of them.
func TestHelpListsEveryFlagOnce(t *testing.T) {
	for name, flags := range commandFlags() {
		listed := map[string]int{}
		for _, g := range flags.help.groups {
			for _, f := range g.flags {
				listed[f]++
				if flags.set.Lookup(f) == nil {
					t.Errorf("%s: the help lists -%s under %q, which is not a flag", name, f, g.heading)
				}
			}
		}
		flags.set.VisitAll(func(f *flag.Flag) {
			if listed[f.Name] != 1 {
				t.Errorf("%s: the help lists -%s %d times, want once", name, f.Name, listed[f.Name])
			}
		})
	}
}

// TestHelpScreen checks what a help screen says: how to use the command, each
// flag with the name of its value, and the config file that would be read.
func TestHelpScreen(t *testing.T) {
	t.Chdir(t.TempDir())
	for name, flags := range commandFlags() {
		var out bytes.Buffer
		flags.help.write(&out, flags.set, "")
		got := out.String()
		for _, want := range []string{
			"Usage:\n  " + name + " [flags]", "Examples:\n", "\nFolders:\n",
			"  -source <folder>  ", "  -compilations <name>  ", "  -workers <n>  ", "  -dry-run  ", "  -config <file>  ",
			"  -h  ", "\nConfig file:\n  None: no -config was given, and this folder has no config.yaml.\n  Your own config file would be " +
				filepath.Join(os.Getenv("APPDATA"), "tagshelf", "config.yaml") + "\n",
			// The examples and the defaults are written as this system
			// writes a path.
			here(`-dest D:\Music`), here(`<dest>\_Untagged).`),
			filepath.Join(dataFolderName(), "libraries", "<library-id>", "tagshelf.db") + ").",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: the help lacks %q:\n%s", name, want, got)
			}
		}
		if strings.Contains(got, "`") {
			t.Errorf("%s: the help shows a backquote of a usage text:\n%s", name, got)
		}
		// The config file's path is as long as it is.
		flagsPart, _, _ := strings.Cut(got, "\nConfig file:\n")
		for _, line := range strings.Split(flagsPart, "\n") {
			// The default database path is one word, longer than a line.
			if len(line) > helpWidth && !strings.Contains(line, "<library-id>") {
				t.Errorf("%s: a line of help is %d wide, want at most %d: %q", name, len(line), helpWidth, line)
			}
		}
	}
}

// TestHelpNamesTheConfigFile checks the last lines of the help: the config
// file named with -config, or else the one found in the working directory.
func TestHelpNamesTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	note := func(configPath string) string {
		var out bytes.Buffer
		flags := commandFlags()["tagshelf"]
		flags.help.write(&out, flags.set, configPath)
		_, after, _ := strings.Cut(out.String(), "\nConfig file:\n  ")
		return strings.TrimSpace(after)
	}
	if got, want := note("mine.yaml"), "mine.yaml (named with -config)"; got != want {
		t.Errorf("with -config: %q, want %q", got, want)
	}
	writeFile(t, filepath.Join(dir, "config.yaml"), "dry_run: true\n")
	got := note("")
	if !strings.HasSuffix(got, "config.yaml (found in this folder)") || !filepath.IsAbs(strings.TrimSuffix(got, " (found in this folder)")) {
		t.Errorf("with a config.yaml in the folder: %q, want its full path and where it was found", got)
	}
}

// TestRunWithNoArgumentsOnlyShowsHelp runs tagshelf with nothing typed in a
// folder whose config.yaml is all a real run needs: the help is printed on
// standard output, and the config file starts no run.
func TestRunWithNoArgumentsOnlyShowsHelp(t *testing.T) {
	tmp, _, logs := cliEnv(t)
	var out bytes.Buffer
	stdout = &out
	song := filepath.Join(tmp, "src", "loose.mp3")
	writeFile(t, song, "no tags")
	lib := filepath.Join(tmp, "lib")
	writeFile(t, filepath.Join(tmp, "config.yaml"), "source: '"+filepath.Join(tmp, "src")+"'\ndestination: '"+lib+"'\ndry_run: false\n")
	t.Chdir(tmp)

	if got := run(context.Background(), nil); got != exitOK {
		t.Errorf("run() = %d, want %d", got, exitOK)
	}
	if !strings.Contains(out.String(), "Usage:\n  tagshelf [flags]") || !strings.Contains(out.String(), "config.yaml (found in this folder)") {
		t.Errorf("standard output lacks the help, or the config file it found:\n%s", out.String())
	}
	if logs.Len() != 0 {
		t.Errorf("help that was asked for went to the log too: %q", logs)
	}
	if _, err := os.Stat(song); err != nil {
		t.Errorf("the song moved: %v", err)
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		t.Errorf("the library folder was made: %v", err)
	}

	// With a flag typed, the same config file does start the run.
	if got := run(context.Background(), []string{"-verbose"}); got != exitOK {
		t.Errorf("run(-verbose) = %d, want %d\n%s", got, exitOK, logs)
	}
	if _, err := os.Stat(song); !os.IsNotExist(err) {
		t.Errorf("the run with a flag left the song: %v", err)
	}
}

// TestHelpGoesWhereItWasAskedFor checks that -h prints the help on standard
// output, and a mistake prints it where the mistake is reported: before it,
// so that the mistake is the last thing on the screen.
func TestHelpGoesWhereItWasAskedFor(t *testing.T) {
	for _, command := range [][]string{nil, {"tui"}} {
		name := strings.Join(append([]string{"tagshelf"}, command...), " ")
		usage := "Usage:\n  " + name + " [flags]"
		_, config, logs := cliEnv(t)
		var out bytes.Buffer
		stdout = &out

		if got := run(context.Background(), append(command, "-h")); got != exitOK {
			t.Errorf("%s -h = %d, want %d", name, got, exitOK)
		}
		if !strings.Contains(out.String(), usage) || logs.Len() != 0 {
			t.Errorf("%s -h: standard output %q, log %q; want the help on standard output alone", name, out.String(), logs)
		}

		mistakes := []struct {
			args []string
			last string // the last two lines printed
		}{
			{[]string{"-config", config, "-nope"}, "tagshelf cannot start: there is no flag -nope.\nThe flags are listed above.\n"},
			{[]string{"-config", config, "-workers"}, "tagshelf cannot start: the flag -workers needs a value after it.\nThe flags are listed above.\n"},
			{[]string{"-config", config, "-dry-run", "yes"}, "tagshelf cannot start: unexpected argument \"yes\": " + name + " takes only flags.\n" +
				"A flag that is on or off is written -flag or -flag=false, with no value after it.\n"},
		}
		if command == nil {
			// The TUI opens without a library folder, and says so itself.
			mistakes = append(mistakes, struct {
				args []string
				last string
			}{[]string{"-config", config, "-source", t.TempDir()}, "tagshelf cannot start: no library folder is set.\n" + here(noDestinationTodo) + "\n"})
		}
		for _, m := range mistakes {
			out.Reset()
			logs.Reset()
			if got := run(context.Background(), append(command, m.args...)); got != exitCannotStart {
				t.Errorf("%s %q = %d, want %d", name, m.args, got, exitCannotStart)
			}
			got := logs.String()
			if !strings.HasPrefix(got, flagsHelpStart(name)) || !strings.Contains(got, usage) || !strings.HasSuffix(got, "\n\n"+m.last) {
				t.Errorf("%s %q printed\n%s\nwant the help, an empty line and then\n%s", name, m.args, got, m.last)
			}
			if !strings.Contains(got, config+" (named with -config)") {
				t.Errorf("%s %q: the help does not name the config file given", name, m.args)
			}
			if out.Len() != 0 {
				t.Errorf("%s %q printed on standard output: %q", name, m.args, out.String())
			}
		}
	}
}

// flagsHelpStart is how the help of the command begins.
func flagsHelpStart(command string) string {
	if command == "tagshelf tui" {
		return "tagshelf tui opens the terminal UI"
	}
	return "tagshelf sorts music files"
}

// TestProblemsShowNoHelp checks the refusals of a command that was typed
// right: they say what is wrong and what to do, in two lines without a
// timestamp, and the help is not printed over them.
func TestProblemsShowNoHelp(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T, tmp string) []string
		want []string
	}{
		{"a config file that is not there", func(t *testing.T, tmp string) []string {
			return []string{"-config", filepath.Join(tmp, "none.yaml"), "-dest", tmp}
		}, []string{"tagshelf cannot start: the config file " + "TMP" + `\none.yaml cannot be read: `, "\nFix the file, or name another one with -config.\n"}},
		{"a config file with a key tagshelf does not know", func(t *testing.T, tmp string) []string {
			writeFile(t, filepath.Join(tmp, "bad.yaml"), "dry_run: true\ndry-run: true\n")
			return []string{"-config", filepath.Join(tmp, "bad.yaml"), "-dest", tmp}
		}, []string{"tagshelf cannot start: the config file " + "TMP" + `\bad.yaml has an unknown key "dry-run" on line 2 (the keys are `, "\nFix the file, or name another one with -config.\n"}},
		{"a config file that is not YAML", func(t *testing.T, tmp string) []string {
			writeFile(t, filepath.Join(tmp, "bad.yaml"), "dry_run: [\n")
			return []string{"-config", filepath.Join(tmp, "bad.yaml"), "-dest", tmp}
		}, []string{"tagshelf cannot start: the config file " + "TMP" + `\bad.yaml is not valid YAML: `}},
		{"a source folder that is not there", func(t *testing.T, tmp string) []string {
			return []string{"-config", filepath.Join(tmp, "empty.yaml"), "-source", filepath.Join(tmp, "nope"), "-dest", filepath.Join(tmp, "lib")}
		}, []string{"tagshelf cannot start: the source folder " + "TMP" + `\nope does not exist.` + "\nCheck the path given with -source, or as source in the config file.\n"}},
		{"a source that is a file", func(t *testing.T, tmp string) []string {
			return []string{"-config", filepath.Join(tmp, "empty.yaml"), "-source", filepath.Join(tmp, "empty.yaml"), "-dest", filepath.Join(tmp, "lib")}
		}, []string{"tagshelf cannot start: the source " + "TMP" + `\empty.yaml is a file, not a folder.`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, _, logs := cliEnv(t)
			args := tt.args(t, tmp)
			logs.Reset()
			if got := run(context.Background(), args); got != exitCannotStart {
				t.Errorf("run(%q) = %d, want %d", args, got, exitCannotStart)
			}
			got := logs.String()
			if !strings.HasPrefix(got, "tagshelf cannot start: ") || strings.Contains(got, "Usage:") {
				t.Errorf("printed %q, want it to begin with the refusal and show no help", got)
			}
			if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 1 {
				t.Errorf("printed %d lines, want two: %q", n+1, got)
			}
			for _, want := range tt.want {
				want = strings.NewReplacer(`TMP\`, tmp+string(filepath.Separator), "TMP", tmp).Replace(want)
				if !strings.Contains(got, want) {
					t.Errorf("printed %q, which lacks %q", got, want)
				}
			}
			if _, err := os.Stat(filepath.Join(tmp, "lib")); !os.IsNotExist(err) {
				t.Errorf("the refused run made the library folder: %v", err)
			}
		})
	}
}

// TestIndexNeedsNoSourceFolder checks that the source is not looked at by an
// index run, which reads only the library.
func TestIndexNeedsNoSourceFolder(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	args := []string{"-config", config, "-source", filepath.Join(tmp, "nope"), "-dest", filepath.Join(tmp, "lib"), "-index"}
	if got := run(context.Background(), args); got != exitOK {
		t.Errorf("run(%q) = %d, want %d\n%s", args, got, exitOK, logs)
	}
}

// TestTUIProblemIsNotPrintedAsARefusal checks that a TUI that opened and
// then failed is not reported as one that could not start.
func TestTUIProblemIsNotPrintedAsARefusal(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	if got := run(context.Background(), []string{"-config", config, "-dest", lib, "-index"}); got != exitOK {
		t.Fatalf("-index = %d\n%s", got, logs)
	}
	logs.Reset()
	real := showTUI
	defer func() { showTUI = real }()
	showTUI = func(context.Context, tui.Options) error { return errors.New("the terminal went away") }
	if got := run(context.Background(), []string{"tui", "-config", config, "-dest", lib}); got != exitErrors {
		t.Errorf("exit code = %d, want %d", got, exitErrors)
	}
	if got, want := logs.String(), "The terminal UI stopped: the terminal went away.\n"; got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
}

func TestFlagProblem(t *testing.T) {
	for msg, want := range map[string]string{
		"flag provided but not defined: -x":                "there is no flag -x",
		"flag needs an argument: -source":                  "the flag -source needs a value after it",
		`invalid value "x" for flag -workers: parse error`: `invalid value "x" for flag -workers: parse error`,
	} {
		if got := flagProblem(errors.New(msg)); got != want {
			t.Errorf("flagProblem(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestWrap(t *testing.T) {
	tests := []struct {
		text  string
		width int
		want  []string
	}{
		{"one two three", 20, []string{"one two three"}},
		{"one two three", 7, []string{"one two", "three"}},
		{"one two three", 3, []string{"one", "two", "three"}},
		{"a longerword b", 4, []string{"a", "longerword", "b"}},
		{"", 10, []string{""}},
	}
	for _, tt := range tests {
		if got := wrap(tt.text, tt.width); strings.Join(got, "|") != strings.Join(tt.want, "|") {
			t.Errorf("wrap(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
		}
	}
}

// TestDataFolderName checks how the folder of the databases is written for a
// reader: by its variable on Windows, and elsewhere the folder itself, with ~
// for the home folder when it is under it.
func TestDataFolderName(t *testing.T) {
	if runtime.GOOS == "windows" {
		if got := dataFolderName(); got != `%LOCALAPPDATA%\tagshelf` {
			t.Errorf("dataFolderName() = %q", got)
		}
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	if got, want := dataFolderName(), "~/.local/share/tagshelf"; got != want {
		t.Errorf("dataFolderName() = %q, want %q", got, want)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if got, want := dataFolderName(), "~/data/tagshelf"; got != want {
		t.Errorf("with XDG_DATA_HOME under the home folder: %q, want %q", got, want)
	}
	elsewhere := t.TempDir()
	t.Setenv("XDG_DATA_HOME", elsewhere)
	if got, want := dataFolderName(), filepath.Join(elsewhere, "tagshelf"); got != want {
		t.Errorf("with XDG_DATA_HOME elsewhere: %q, want %q", got, want)
	}
}
