package engine

import (
	"context"
	"path/filepath"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

// TestIndexKeepsEveryGenreAndComposer indexes a file of each tag format with
// two genres and two composers: the row holds both of each, joined, where the
// tag reader's own Genre and Composer give only the first.
func TestIndexKeepsEveryGenreAndComposer(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	var paths []string
	for _, ext := range fixtures.Extensions() {
		path := filepath.Join(lib, "_Untagged", "song"+ext)
		writeTestFile(t, path, readTestFile(t, fixtures.Tagged(ext)))
		if err := taglib.WriteTags(path, map[string][]string{taglib.Genre: {"Rock", "Hip Hop"}, taglib.Composer: {"Ann", "Bob"}}, 0); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		paths = append(paths, path)
	}
	s := openStore(t)
	Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s})

	rows := indexedFiles(t, s)
	for _, path := range paths {
		if got := rows[path]; got.Genre != "Rock; Hip Hop" || got.Composer != "Ann; Bob" {
			t.Errorf("%s: genre %q, composer %q; want both values of each", filepath.Base(path), got.Genre, got.Composer)
		}
	}
}

// TestIndexFillsInRowsFromBeforeItKeptGenres checks that a row read before
// the index had the two columns gets them on the next run from the tags it
// already holds: the file is not read again.
func TestIndexFillsInRowsFromBeforeItKeptGenres(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	path := filepath.Join(lib, "_Untagged", "song.m4a")
	writeTestFile(t, path, readTestFile(t, fixtures.Tagged(".m4a")))
	if err := taglib.WriteTags(path, map[string][]string{taglib.Genre: {"Rock", "Hip Hop"}, taglib.Composer: {"Ann"}}, 0); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(lib, "_Untagged", "plain.mp3")
	writeTestFile(t, plain, readTestFile(t, fixtures.Untagged(".mp3")))
	s := openStore(t)
	Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s})
	execSQL(t, s, `UPDATE files SET genre = NULL, composer = NULL`)
	if got := indexedFiles(t, s)[path]; got.Genre != "" || got.Composer != "" {
		t.Fatalf("the row from before reads genre %q, composer %q; want none yet", got.Genre, got.Composer)
	}

	var c collector
	Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: c.sink})

	if got := indexedFiles(t, s)[path]; got.Genre != "Rock; Hip Hop" || got.Composer != "Ann" {
		t.Errorf("after the run: genre %q, composer %q", got.Genre, got.Composer)
	}
	if p, ok := c.indexPhase(); ok && p.Files != 0 {
		t.Errorf("the run read %d files again, want none", p.Files)
	}
	rows, err := s.FilesWithoutLists()
	if err != nil || len(rows) != 0 {
		t.Errorf("%d rows are still without the columns (%v): a file without tags has none to wait for", len(rows), err)
	}
}
