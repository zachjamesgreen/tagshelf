package store

import (
	"path/filepath"
	"slices"
	"strings"
)

// likeEscaper escapes what LIKE treats as a wildcard, for ESCAPE '\'.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// SearchLibrary lists up to limit indexed files of the library whose artist,
// album artist, album or title holds every word of query, whatever its case,
// in artist, album, disc and track order. A query without words finds
// nothing.
func (s *Store) SearchLibrary(query string, limit int) ([]File, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 || limit <= 0 {
		return nil, nil
	}
	// The words are matched here rather than with LIKE, which only knows the
	// case of ASCII letters.
	rows, err := s.db.Query(`SELECT f.id, f.artist, f.album_artist, f.album, f.title FROM files f JOIN roots r ON r.id = f.root_id
		WHERE r.kind = ?
		ORDER BY CASE WHEN f.album_artist <> '' THEN f.album_artist ELSE f.artist END COLLATE NOCASE,
		f.album COLLATE NOCASE, f.disc, f.track, f.path_key`, string(RootLibrary))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() && len(ids) < limit {
		var id int64
		var artist, albumArtist, album, title string
		if err := rows.Scan(&id, &artist, &albumArtist, &album, &title); err != nil {
			return nil, err
		}
		// A separator no tag holds keeps a word from matching across fields.
		tags := strings.ToLower(artist + "\x00" + albumArtist + "\x00" + album + "\x00" + title)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(tags, w) }) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	files, err := s.FilesByID(ids)
	if err != nil {
		return nil, err
	}
	// FilesByID lists by ID; the search's order is that of ids.
	at := make(map[int64]int, len(ids))
	for i, id := range ids {
		at[id] = i
	}
	slices.SortFunc(files, func(a, b File) int { return at[a.ID] - at[b.ID] })
	return files, nil
}

// FolderFiles lists the indexed files in the same folder as f, f included,
// in path order: in the library, the files of its album.
func (s *Store) FolderFiles(f File) ([]File, error) {
	dir := filepath.Dir(f.RelPath)
	q := `SELECT ` + fileColumns + ` FROM files f JOIN roots r ON r.id = f.root_id WHERE f.root_id = ?`
	args := []any{f.RootID}
	if dir != "." {
		q += ` AND f.path_key LIKE ? ESCAPE '\'`
		args = append(args, likeEscaper.Replace(PathKey(dir)+string(filepath.Separator))+"%")
	}
	files, err := s.queryFiles(q+` ORDER BY f.path_key`, args...)
	if err != nil {
		return nil, err
	}
	// The prefix also matched the folders below.
	in := files[:0]
	for _, o := range files {
		if PathKey(filepath.Dir(o.RelPath)) == PathKey(dir) {
			in = append(in, o)
		}
	}
	return in, nil
}
