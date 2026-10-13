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
}
