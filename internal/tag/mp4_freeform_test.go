package tag

import (
	"os"
	"testing"

	"tagshelf/internal/fixtures"
)

// TestMP4FreeFormValuesSkipLocale is the regression test for "----" atom
// values, which used to keep the 4-byte locale of their data atom as four
// leading NULs.
func TestMP4FreeFormValuesSkipLocale(t *testing.T) {
	for _, name := range []string{"tagged.m4a", "tagged.m4b"} {
		f, err := os.Open(fixtures.Path(name))
		if err != nil {
			t.Fatal(err)
		}
		m, err := ReadFrom(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Raw()["MusicBrainz Album Id"]; got != fixtures.ReleaseID {
			t.Errorf("%s: MusicBrainz Album Id = %q, want %q", name, got, fixtures.ReleaseID)
		}
	}
}
