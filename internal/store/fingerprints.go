package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Fingerprint is what listening to a file gave, kept by the hash of its
// audio.
type Fingerprint struct {
	// Sum is the hash of the audio, as File.Sum has it.
	Sum string
	// Duration is the length of the file in whole seconds.
	Duration int
	// Fingerprint is the AcoustID fingerprint.
	Fingerprint string
	// Answer is what AcoustID answered for it, as JSON, nil when it has not
	// been asked. Answered is when.
	Answer   []byte
	Answered time.Time
}

// Fingerprint returns what is on record for the audio that hashes to sum.
// ok is false when there is nothing.
func (s *Store) Fingerprint(sum string) (fp Fingerprint, ok bool, err error) {
	if sum == "" {
		return Fingerprint{}, false, nil
	}
	var answer, answered sql.NullString
	fp.Sum = sum
	err = s.db.QueryRow(`SELECT duration, fingerprint, answer, answered_at FROM fingerprints WHERE audio_sum = ?`, sum).
		Scan(&fp.Duration, &fp.Fingerprint, &answer, &answered)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Fingerprint{}, false, nil
	case err != nil:
		return Fingerprint{}, false, fmt.Errorf("reading the fingerprint on record: %w", err)
	}
	if answer.Valid && answered.Valid {
		at, err := parseTime(answered)
		if err == nil {
			fp.Answer, fp.Answered = []byte(answer.String), at
		}
	}
	return fp, true, nil
}

// SetFingerprint puts the fingerprint of the audio that hashes to fp.Sum on
// record, with AcoustID's answer when fp has one. A fingerprint recorded
// without an answer keeps the answer the record had for the same
// fingerprint.
func (s *Store) SetFingerprint(fp Fingerprint, at time.Time) error {
	if fp.Sum == "" || fp.Fingerprint == "" {
		return errors.New("recording a fingerprint: it needs the hash of the audio and the fingerprint")
	}
	var answer, answered any
	if fp.Answer != nil {
		answer, answered = string(fp.Answer), formatTime(fp.Answered)
	}
	_, err := s.db.Exec(`INSERT INTO fingerprints (audio_sum, duration, fingerprint, made_at, answer, answered_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (audio_sum) DO UPDATE SET
			answer = CASE WHEN excluded.answer IS NOT NULL THEN excluded.answer
				WHEN fingerprints.fingerprint = excluded.fingerprint THEN fingerprints.answer END,
			answered_at = CASE WHEN excluded.answer IS NOT NULL THEN excluded.answered_at
				WHEN fingerprints.fingerprint = excluded.fingerprint THEN fingerprints.answered_at END,
			duration = excluded.duration, fingerprint = excluded.fingerprint`,
		fp.Sum, fp.Duration, fp.Fingerprint, formatTime(at), answer, answered)
	if err != nil {
		return fmt.Errorf("recording the fingerprint: %w", err)
	}
	return nil
}
