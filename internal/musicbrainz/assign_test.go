package musicbrainz

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// pos names a track as "disc-track", such as "2-3".
func pos(rel *Release, ref TrackRef) string {
	return fmt.Sprintf("%d-%d", rel.Media[ref.Medium].Position, rel.Track(ref).Position)
}

func sec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// placed is a mapping in short form: track of each assigned file, with its
// length grade.
type placed struct {
	track  string
	length LengthMatch
}

func summarize(rel *Release, m *Mapping) (map[int]placed, []int, int) {
	got := map[int]placed{}
	for _, a := range m.Assignments {
		got[a.File] = placed{pos(rel, a.Track), a.Length}
	}
	return got, m.Unassigned, len(m.EmptyTracks)
}

func TestAssignDeluxe(t *testing.T) {
	rel := lookupFixture(t, "release-dua-lipa-deluxe.json")
	if n := len(rel.Tracks()); n != 17 {
		t.Fatalf("deluxe has %d tracks, want 17", n)
	}

	tests := []struct {
		desc       string
		files      []FileInfo
		want       map[int]placed
		unassigned []int
	}{
		{
			"partial selection across both discs",
			[]FileInfo{
				{Title: "IDGAF", Length: sec(217)},
				{Title: "Dreams", Length: sec(222.4)},
				{Title: "homesick", Length: sec(228)},
			},
			map[int]placed{0: {"1-5", LengthClose}, 1: {"2-1", LengthClose}, 2: {"1-12", LengthClose}},
			nil,
		},
		{
			"numbered files without titles, disc tags pick the disc",
			[]FileInfo{
				{Path: "2-03 Track 03.flac", Disc: 2, Track: 3, Length: sec(234)},
				{Path: "1-03 Track 03.flac", Disc: 1, Track: 3, Length: sec(187)},
			},
			map[int]placed{0: {"2-3", LengthClose}, 1: {"1-3", LengthClose}},
			nil,
		},
		{
			"numbered straight through the release",
			[]FileInfo{{Path: "15 Track 15.mp3", Track: 15, Length: sec(234)}},
			map[int]placed{0: {"2-3", LengthClose}},
			nil,
		},
		{
			"length differences grade and flag",
			[]FileInfo{
				{Title: "Genesis", Length: sec(206 + 5)},
				// 2 s from No Goodbyes (214 s), but the title keeps it on Garden.
				{Title: "Garden", Length: sec(227 - 15)},
				{Title: "Begging", Length: sec(193 + 3)},
				{Title: "Be the One"},
			},
			map[int]placed{
				0: {"1-1", LengthWeak},
				1: {"1-7", LengthFlagged},
				2: {"1-11", LengthClose},
				3: {"1-4", LengthUnknown},
			},
			nil,
		},
		{
			"a file that fits no track stays unassigned",
			[]FileInfo{
				{Title: "New Rules", Length: sec(209)},
				{Title: "Something Else Entirely", Artist: "Nobody"},
				{},
			},
			map[int]placed{0: {"1-10", LengthClose}},
			[]int{1, 2},
		},
		{
			"two files for one track: the closer length wins",
			[]FileInfo{
				{Title: "New Rules", Length: sec(250)},
				{Title: "New Rules", Length: sec(209)},
			},
			map[int]placed{1: {"1-10", LengthClose}},
			[]int{0},
		},
	}
	for _, tt := range tests {
		m := Assign(rel, tt.files)
		got, unassigned, empty := summarize(rel, m)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: assigned %v, want %v", tt.desc, got, tt.want)
		}
		if !reflect.DeepEqual(unassigned, tt.unassigned) {
			t.Errorf("%s: unassigned %v, want %v", tt.desc, unassigned, tt.unassigned)
		}
		if want := 17 - len(tt.want); empty != want {
			t.Errorf("%s: %d empty tracks, want %d", tt.desc, empty, want)
		}
	}
}

func TestAssignMoreFilesThanTracks(t *testing.T) {
	rel := &Release{Media: []Medium{{Position: 1, TrackCount: 2, Tracks: []Track{
		{ID: "t1", Position: 1, Title: "One", Length: 100000},
		{ID: "t2", Position: 2, Title: "Two", Length: 200000},
	}}}}
	files := []FileInfo{
		{Title: "Two", Length: sec(200)},
		{Title: "Three", Length: sec(300)},
		{Title: "One", Length: sec(100)},
	}
	got, unassigned, empty := summarize(rel, Assign(rel, files))
	want := map[int]placed{0: {"1-2", LengthClose}, 2: {"1-1", LengthClose}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(unassigned, []int{1}) || empty != 0 {
		t.Errorf("got %v, unassigned %v, %d empty", got, unassigned, empty)
	}
}

func TestPairLength(t *testing.T) {
	rel := &Release{Media: []Medium{{Position: 1, Tracks: []Track{
		{Position: 1, Title: "Song", Length: 200000},
		{Position: 2, Title: "Song", Recording: &TrackRecording{Length: 100000}},
		{Position: 3, Title: "Song"},
	}}}}
	tests := []struct {
		desc   string
		index  int
		length time.Duration
		grade  LengthMatch
		delta  time.Duration
		score  int
	}{
		{"exact", 0, sec(200), LengthClose, 0, 100},
		{"3 s is still close", 0, sec(203), LengthClose, -sec(3), 100},
		// Past 3 s the length part falls linearly to 0 at 10 s: 6.5 s is
		// half, so (13 + 5)/23.
		{"weak", 0, sec(193.5), LengthWeak, sec(6.5), 78},
		{"10 s is weak", 0, sec(210), LengthWeak, -sec(10), 57},
		{"flagged past 10 s", 0, sec(210.001), LengthFlagged, -sec(10.001), 57},
		{"recording length stands in", 1, sec(100), LengthClose, 0, 100},
		{"no track length", 2, sec(100), LengthUnknown, 0, 100},
		{"no file length", 0, 0, LengthUnknown, 0, 100},
	}
	for _, tt := range tests {
		a := Pair(rel, TrackRef{Index: tt.index}, FileInfo{Title: "Song", Length: tt.length})
		if a.Length != tt.grade || a.LengthDelta != tt.delta || a.Score != tt.score {
			t.Errorf("%s: got %v %v score %d, want %v %v score %d", tt.desc, a.Length, a.LengthDelta, a.Score, tt.grade, tt.delta, tt.score)
		}
		if a.Flagged() != (tt.grade == LengthFlagged) {
			t.Errorf("%s: Flagged() = %v", tt.desc, a.Flagged())
		}
	}
}

func TestMappingReassign(t *testing.T) {
	rel := lookupFixture(t, "release-dua-lipa-deluxe.json")
	files := []FileInfo{
		{Title: "Genesis", Length: sec(206)},
		{Title: "Garden", Length: sec(227)},
		{Title: "Mystery"},
	}
	m := Assign(rel, files)
	if !reflect.DeepEqual(m.Unassigned, []int{2}) {
		t.Fatalf("unassigned = %v", m.Unassigned)
	}

	// The unknown file goes onto Garden's track, which unassigns Garden.
	garden := TrackRef{Medium: 0, Index: 6}
	m.Reassign(2, garden)
	got, unassigned, empty := summarize(rel, m)
	if want := (map[int]placed{0: {"1-1", LengthClose}, 2: {"1-7", LengthUnknown}}); !reflect.DeepEqual(got, want) {
		t.Errorf("after reassign: %v, want %v", got, want)
	}
	if !reflect.DeepEqual(unassigned, []int{1}) || empty != 15 {
		t.Errorf("after reassign: unassigned %v, %d empty", unassigned, empty)
	}

	// Garden (227 s) moves to track 2 (204 s): flagged.
	m.Reassign(1, TrackRef{Medium: 0, Index: 1})
	if f := m.Flagged(); len(f) != 1 || f[0].File != 1 || pos(rel, f[0].Track) != "1-2" {
		t.Errorf("flagged = %+v", f)
	}
	// Assignments stay in track order.
	var order []string
	for _, a := range m.Assignments {
		order = append(order, pos(rel, a.Track))
	}
	if want := []string{"1-1", "1-2", "1-7"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}

	m.Unassign(0)
	if !reflect.DeepEqual(m.Unassigned, []int{0}) || len(m.Assignments) != 2 || len(m.EmptyTracks) != 15 {
		t.Errorf("after unassign: %+v", m)
	}
}

func TestHungarian(t *testing.T) {
	tests := []struct {
		cost [][]float64
		want []int
	}{
		{nil, nil},
		{[][]float64{{5}}, []int{0}},
		{[][]float64{{1, 2}, {2, 4}}, []int{1, 0}},
		{[][]float64{{4, 1, 3}, {2, 0, 5}, {3, 2, 2}}, []int{1, 0, 2}},
		{[][]float64{{-0.9, -0.8}, {-0.8, 0}}, []int{1, 0}},
	}
	for _, tt := range tests {
		if got := hungarian(tt.cost); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("hungarian(%v) = %v, want %v", tt.cost, got, tt.want)
		}
	}
}
