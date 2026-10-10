package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// ownFolding says that tagshelf has to treat two names that differ only in
// letter case as one name itself, because the file system does not: that is
// everywhere but on Windows.
//
// On Windows asking for "the beatles" finds "The Beatles", so a library never
// holds both. Elsewhere nothing stops that, and a library with both cannot be
// copied to a disk that ignores case. So inside the folders a run places
// files in, tagshelf never makes a name that differs only in case from one
// that is there: it uses the one that is there (see organizer.spell).
var ownFolding = runtime.GOOS != "windows"

// foldName is the form names are compared in when letter case is ignored.
// It is what rules.PathKey does to a path on Windows.
func foldName(name string) string { return strings.ToLower(name) }

// nameKey is a path in the form two paths that differ only in letter case
// share, on every system.
func nameKey(path string) string { return foldName(filepath.Clean(path)) }

// spelling is one name a folder holds, or is about to.
type spelling struct {
	name string
	dir  bool
}

// names remembers how the folders a run places files in spell what they
// hold, so that a name asked for in another case is found. Each folder is
// listed once, when a name is first looked for in it; what the run plans to
// make in it is added as it goes.
type names struct {
	mu sync.Mutex
	// folders maps a folder, as it is spelled on disk, to its names by their
	// folded form. More than one under a form are twins: names that differ
	// only in case and were there before tagshelf looked.
	folders map[string]map[string][]spelling
	// twins holds the pairs found and not yet reported, and told the folded
	// paths already reported.
	twins []twin
	told  map[string]bool
}

// twin is two names in one folder that differ only in letter case: the one
// tagshelf uses, with its folder, and the other.
type twin struct {
	used, other string
}

// list returns the names of the folder dir, reading it the first time.
// A folder that is not there, or cannot be read, has none.
func (n *names) list(dir string) map[string][]spelling {
	if n.folders == nil {
		n.folders = make(map[string]map[string][]spelling)
	}
	if byName, ok := n.folders[dir]; ok {
		return byName
	}
	byName := make(map[string][]spelling)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		key := foldName(e.Name())
		byName[key] = append(byName[key], spelling{name: e.Name(), dir: e.IsDir()})
	}
	for _, same := range byName {
		// The name that sorts first is the one used: capitals come first.
		slices.SortFunc(same, func(a, b spelling) int { return strings.Compare(a.name, b.name) })
	}
	n.folders[dir] = byName
	return byName
}

// find returns how the folder dir spells name, when it holds it in any
// letter case. Of twins it takes the first, and for the name of a folder the
// first that is a folder.
func (n *names) find(dir, name string, folder bool) (string, bool) {
	same := n.list(dir)[foldName(name)]
	if len(same) == 0 {
		return "", false
	}
	pick := same[0]
	if folder {
		for _, s := range same {
			if s.dir {
				pick = s
				break
			}
		}
	}
	if len(same) > 1 {
		key := nameKey(filepath.Join(dir, name))
		if n.told == nil {
			n.told = make(map[string]bool)
		}
		if !n.told[key] {
			n.told[key] = true
			for _, s := range same {
				if s.name != pick.name {
					n.twins = append(n.twins, twin{used: filepath.Join(dir, pick.name), other: s.name})
				}
			}
		}
	}
	return pick.name, true
}

// resolve gives rel, a path below the folder root, as the folders spell it:
// each part that is there in another letter case is taken as it is there.
// With plan, a part that is not there is noted as about to be made, so that
// the next path asking for it in another case gets this one's spelling.
func (n *names) resolve(root, rel string, plan bool) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		last := i == len(parts)-1
		if name, ok := n.find(dir, part, !last); ok {
			part = name
		} else if plan {
			byName := n.list(dir)
			byName[foldName(part)] = []spelling{{name: part, dir: !last}}
		}
		dir = filepath.Join(dir, part)
	}
	return dir
}

// renamed notes that the name old in the folder dir is now spelled name, and
// forgets what was listed below it under its old spelling.
func (n *names) renamed(dir, old, name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	byName := n.list(dir)
	key := foldName(name)
	for i, s := range byName[key] {
		if s.name == old {
			byName[key][i].name = name
		}
	}
	gone := filepath.Join(dir, old)
	for folder := range n.folders {
		if folder == gone || strings.HasPrefix(folder, gone+string(filepath.Separator)) {
			delete(n.folders, folder)
		}
	}
}

// takeTwins returns the twins found since it was last called.
func (n *names) takeTwins() []twin {
	n.mu.Lock()
	defer n.mu.Unlock()
	twins := n.twins
	n.twins = nil
	return twins
}

// outputRoot returns the folder the run places files in that path lies
// below: the library, or the untagged, other or duplicates folder, whichever
// is nearest. ok is false for a path that is in none of them.
func (o *organizer) outputRoot(path string) (root string, ok bool) {
	for _, r := range []string{o.cfg.Destination, o.cfg.Untagged, o.cfg.Other, o.cfg.Duplicates} {
		if r != "" && isSubdirectory(r, path) && len(r) > len(root) {
			root, ok = r, true
		}
	}
	return root, ok
}

// spell gives path, where a run is about to put a file, the spelling its
// folders have on disk: a folder or a file name that is there already in
// another letter case is used as it is, in place of a second one beside it.
// What is not there yet is remembered, so that every file of the run asking
// for it gets the same spelling. On Windows, where the file system does all
// this, and for a path outside the folders the run places files in, path
// comes back as it is.
func (o *organizer) spell(path string) string {
	return o.spelled(path, true)
}

// onDisk is spell for a path that is only looked up: nothing is remembered
// for it.
func (o *organizer) onDisk(path string) string {
	return o.spelled(path, false)
}

func (o *organizer) spelled(path string, plan bool) string {
	if !ownFolding {
		return path
	}
	root, ok := o.outputRoot(path)
	if !ok {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return path
	}
	out := o.names.resolve(root, rel, plan)
	for _, t := range o.names.takeTwins() {
		o.emit(Warning{Kind: WarnCaseTwins, Path: t.used, Name: t.other})
	}
	return out
}
