package store

import (
	"testing"
	"time"
)

// A fingerprint is kept by the hash of the audio. AcoustID's answer joins it
// later, stays when the same fingerprint is recorded again without one, and
// goes when the audio's fingerprint is another.
func TestFingerprints(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	if _, ok, err := s.Fingerprint("sum-1"); ok || err != nil {
		t.Fatalf("an empty record has a fingerprint: %t, %v", ok, err)
	}
	if _, ok, err := s.Fingerprint(""); ok || err != nil {
		t.Errorf("audio without a hash has a fingerprint: %t, %v", ok, err)
	}
	if err := s.SetFingerprint(Fingerprint{Sum: "", Fingerprint: "AQAA"}, at); err == nil {
		t.Error("a fingerprint was recorded for audio without a hash")
	}

	if err := s.SetFingerprint(Fingerprint{Sum: "sum-1", Duration: 187, Fingerprint: "AQAA"}, at); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Fingerprint("sum-1")
	if err != nil || !ok || got.Duration != 187 || got.Fingerprint != "AQAA" || got.Answer != nil || !got.Answered.IsZero() {
		t.Fatalf("Fingerprint = %+v, %t, %v", got, ok, err)
	}

	asked := at.Add(time.Minute)
	if err := s.SetFingerprint(Fingerprint{Sum: "sum-1", Duration: 187, Fingerprint: "AQAA", Answer: []byte(`[{"id":"a"}]`), Answered: asked}, asked); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Fingerprint("sum-1"); string(got.Answer) != `[{"id":"a"}]` || !got.Answered.Equal(asked) {
		t.Errorf("with an answer: %+v", got)
	}
	// The same fingerprint again, without an answer: the one on record stays.
	if err := s.SetFingerprint(Fingerprint{Sum: "sum-1", Duration: 187, Fingerprint: "AQAA"}, asked.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Fingerprint("sum-1"); string(got.Answer) != `[{"id":"a"}]` || !got.Answered.Equal(asked) {
		t.Errorf("the answer on record did not stay: %+v", got)
	}
	// Another fingerprint for the audio: the answer was for the old one.
	if err := s.SetFingerprint(Fingerprint{Sum: "sum-1", Duration: 188, Fingerprint: "AQAB"}, asked.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Fingerprint("sum-1"); got.Fingerprint != "AQAB" || got.Duration != 188 || got.Answer != nil {
		t.Errorf("after another fingerprint: %+v", got)
	}
	// An answer that is not JSON is refused by the database.
	if err := s.SetFingerprint(Fingerprint{Sum: "sum-2", Duration: 1, Fingerprint: "AQAA", Answer: []byte(`not json`), Answered: asked}, asked); err == nil {
		t.Error("an answer that is not JSON was recorded")
	}

	// Several at once are all kept, or none of them.
	many := []Fingerprint{{Sum: "sum-3", Duration: 3, Fingerprint: "AQAC"}, {Sum: "sum-4", Duration: 4, Fingerprint: "AQAD", Answer: []byte(`[]`), Answered: asked}}
	if err := s.SetFingerprints(many, asked); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.Fingerprint("sum-4"); !ok || got.Duration != 4 || string(got.Answer) != `[]` {
		t.Errorf("of several at once: %+v, %t", got, ok)
	}
	if err := s.SetFingerprints([]Fingerprint{{Sum: "sum-5", Duration: 5, Fingerprint: "AQAE"}, {Sum: "", Fingerprint: "AQAF"}}, asked); err == nil {
		t.Error("several were recorded with one that has no hash among them")
	}
	if _, ok, _ := s.Fingerprint("sum-5"); ok {
		t.Error("part of several that were refused was kept")
	}
	if _, ok, _ := s.Fingerprint("sum-3"); !ok {
		t.Error("the first of several is not on record")
	}
}
