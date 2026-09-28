package store

import (
	"errors"
	"testing"
	"time"
)

func TestRecordTagWrite(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.StartRun(KindTUIAction, false, "", `C:\lib`, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	applied := false
	before, after := `{"title":["Old"]}`, `{"title":["New"],"marker":["hand 2026-09-28"]}`
	if err := s.RecordTagWrite(id, `C:\lib\a.flac`, before, after, func() error {
		applied = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Error("RecordTagWrite did not apply the write")
	}

	// A write that fails leaves no row, and its error comes back as it was.
	boom := errors.New("boom")
	if err := s.RecordTagWrite(id, `C:\lib\b.flac`, before, after, func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("RecordTagWrite with a failing write = %v, want %v", err, boom)
	}

	writes, err := s.TagWrites(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 {
		t.Fatalf("tag writes = %+v, want one", writes)
	}
	w := writes[0]
	if w.Path != `C:\lib\a.flac` || w.Before != before || w.After != after || w.At.IsZero() {
		t.Errorf("tag write = %+v, want a.flac with the before and after JSON and a time", w)
	}
}

func TestRecordTagWriteRejectsBadInput(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.StartRun(KindTUIAction, false, "", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	applied := false
	apply := func() error { applied = true; return nil }
	if err := s.RecordTagWrite(id, "a", "not json", "{}", apply); err == nil {
		t.Error("RecordTagWrite accepted before values that are not JSON")
	}
	if err := s.RecordTagWrite(id+1, "a", "{}", "{}", apply); err == nil {
		t.Error("RecordTagWrite accepted a run that does not exist")
	}
	if applied {
		t.Error("RecordTagWrite applied a write it could not record")
	}
}
