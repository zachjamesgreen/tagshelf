package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tagshelf/internal/store"
)

// A tag save or a resolution that names a file in the duplicates folder is
// refused: those files were put aside, and are not the library's to change.
func TestActionsRefuseFilesInTheDuplicatesFolder(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	aside := filepath.Join(lib, "_Duplicates", "copy.mp3")
	writeTestFile(t, aside, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	rows := indexedFiles(t, s)

	saves := 0
	var c collector
	finish := SaveTags(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink}, []int64{rows[aside].ID},
		func(*store.Store, int64, store.File) error { saves++; return nil })
	errs := c.fileErrors()
	if finish.State != StateErrors || saves != 0 || len(errs) != 1 || !strings.Contains(errs[0].Err.Error(), "duplicates folder") {
		t.Errorf("save = %+v after %d saves with errors %+v, want it refused for the duplicates folder", finish, saves, errs)
	}

	finish = ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
		[]Resolution{{Kind: store.GroupExact, Members: []int64{rows[songPath(lib)].ID, rows[aside].ID}, Keeper: rows[aside].ID}})
	if finish.State != StateErrors || finish.Counters.Duplicates != 0 || len(reviews(t, s)) != 0 {
		t.Errorf("resolve = %+v, want it refused", finish)
	}
	mustExist(t, songPath(lib), aside)
}

// A save after which the file cannot be read again is two errors, the index
// and the move, and leaves no row describing tags the file may not have.
func TestSaveTagsWhoseFileCannotBeReadAgain(t *testing.T) {
	lib, ripped, _, _, s := untaggedLibrary(t)
	id := indexedFiles(t, s)[ripped].ID
	var c collector
	finish := SaveTags(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink}, []int64{id},
		func(_ *store.Store, _ int64, f store.File) error { return os.Remove(f.Path) })
	var ops []Operation
	for _, e := range c.fileErrors() {
		ops = append(ops, e.Op)
	}
	if finish.State != StateErrors || len(ops) != 2 || ops[0] != OpIndex || ops[1] != OpOpen {
		t.Errorf("save = %+v with errors in %v, want the index and the open reported", finish, ops)
	}
}

// Index rows that cannot be written are an error of the run, which ends
// without having moved anything it could not record.
func TestIndexRowsThatCannotBeWritten(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, filepath.Join(lib, "in", "song.mp3"), songTags, theAudio)
	s := openStore(t)
	was := afterIndexRead
	t.Cleanup(func() { afterIndexRead = was })
	afterIndexRead = func(string) { s.Close() }

	var c collector
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s, Sink: c.sink})
	if finish.State != StateErrors || finish.Counters.Indexed != 0 {
		t.Errorf("run = %+v, want errors and nothing indexed", finish)
	}
	recorded := c.count(func(e Event) bool {
		fe, ok := e.(FileError)
		return ok && fe.Op == OpRecord
	})
	if recorded == 0 {
		t.Errorf("errors = %+v, want the failed writes reported", c.fileErrors())
	}
}
