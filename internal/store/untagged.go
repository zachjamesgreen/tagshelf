package store

// UntaggedFile is an indexed file in the untagged folder, with where it was
// before a run put it there.
type UntaggedFile struct {
	File
	// From is the path the file had before its latest move into the untagged
	// folder, "" when no run moved it there.
	From string
}

// UntaggedFiles lists the indexed files in the untagged folder, in path
// order.
func (s *Store) UntaggedFiles() ([]UntaggedFile, error) {
	files, err := s.queryFiles(`SELECT `+fileColumns+` FROM files f JOIN roots r ON r.id = f.root_id
		WHERE r.kind = ? ORDER BY f.path_key`, string(RootUntagged))
	if err != nil || len(files) == 0 {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT from_path, to_path FROM moves WHERE reason = ? ORDER BY id`, string(ReasonUntagged))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Later moves to a path overwrite earlier ones.
	from := make(map[string]string)
	for rows.Next() {
		var f, to string
		if err := rows.Scan(&f, &to); err != nil {
			return nil, err
		}
		from[PathKey(to)] = f
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	untagged := make([]UntaggedFile, len(files))
	for i, f := range files {
		untagged[i] = UntaggedFile{File: f, From: from[PathKey(f.Path)]}
	}
	return untagged, nil
}

// ReleaseIDs lists the MusicBrainz release IDs of the indexed files in the
// roots of the given kind.
func (s *Store) ReleaseIDs(kind RootKind) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT f.mb_release_id FROM files f JOIN roots r ON r.id = f.root_id
		WHERE r.kind = ? AND f.mb_release_id != ''`, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
