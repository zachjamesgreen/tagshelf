// Command notices writes THIRD_PARTY_NOTICES: the licence of every module
// built into a download of tagshelf, for any of the systems it is released
// for, and of the tag reader tagshelf carries a copy of. One file goes into
// every download, so it lists what any of them holds.
//
// Run it from the repository root after adding, removing or updating a
// dependency:
//
//	go run ./internal/notices
//
// Versions are left out, so that an update only changes the file when a
// licence text does. CI fails when the file is out of date.
package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const header = `Third-party notices for tagshelf

tagshelf is released under the MIT License (see LICENSE). The program built
from it also contains the software listed below, each under its own licence.

TagLib (https://taglib.org) is included through go.senan.xyz/taglib, which
embeds it compiled to WebAssembly. TagLib is offered under the GNU Lesser
General Public License 2.1 or the Mozilla Public License 1.1; tagshelf uses it
unmodified. To use tagshelf with another build of TagLib, replace the
go.senan.xyz/taglib module and rebuild tagshelf from its source.

To regenerate this file: go run ./internal/notices
`

const rule = "\n================================================================================\n"

func main() {
	log.SetFlags(0)
	var out bytes.Buffer
	out.WriteString(header)

	// The reader in internal/tag began as a copy of github.com/dhowden/tag.
	add(&out, "github.com/dhowden/tag (carried in internal/tag, with changes)", filepath.Join("internal", "tag"))

	for _, m := range modules() {
		add(&out, m.path, m.dir)
	}
	if err := os.WriteFile("THIRD_PARTY_NOTICES", out.Bytes(), 0644); err != nil {
		log.Fatal(err)
	}
}

type module struct{ path, dir string }

// targets are the systems tagshelf is released for, as GOOS and GOARCH. The
// release workflow builds one download for each.
var targets = [][2]string{{"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}}

// modules are the modules whose packages a build of tagshelf for any of the
// targets imports, other than tagshelf itself, sorted by path. The targets
// are named here, so that the file comes out the same on whatever system it
// is made.
func modules() []module {
	seen := map[string]bool{}
	var mods []module
	for _, target := range targets {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Dir}}{{end}}{{end}}", ".")
		cmd.Env = append(os.Environ(), "GOOS="+target[0], "GOARCH="+target[1], "CGO_ENABLED=0")
		cmd.Stderr = os.Stderr
		list, err := cmd.Output()
		if err != nil {
			log.Fatalf("go list for %s/%s: %v", target[0], target[1], err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
			path, dir, ok := strings.Cut(strings.TrimSpace(line), "\t")
			if !ok || seen[path] {
				continue
			}
			seen[path] = true
			mods = append(mods, module{path, dir})
		}
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].path < mods[j].path })
	return mods
}

// add writes the licence files found in dir under the name given.
func add(out *bytes.Buffer, name, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	var files []string
	for _, e := range entries {
		upper := strings.ToUpper(e.Name())
		if e.IsDir() {
			continue
		}
		for _, prefix := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE", "UNLICENSE"} {
			if strings.HasPrefix(upper, prefix) {
				files = append(files, e.Name())
				break
			}
		}
	}
	if len(files) == 0 {
		log.Fatalf("%s: no licence file in %s", name, dir)
	}
	sort.Strings(files)
	for _, f := range files {
		text, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		fmt.Fprintf(out, "%s%s\n%s%s\n", rule, name, f, rule)
		// One form of line ending, whatever the module was packed with.
		out.WriteString(strings.TrimSpace(strings.ReplaceAll(string(text), "\r\n", "\n")))
		out.WriteString("\n")
	}
}
