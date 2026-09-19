package proxytest

import (
	"cmp"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// Record is one record of the spool's second slot, in spool's own type.
type Record = spool.Record

// seedClientVersion is the build a seeded record states it came from.
const seedClientVersion = "seed-build"

// SeedRecord stores one record of the given kind, as the path that
// produces that kind would, and returns the record id it was stored
// under. Every kind reaches the spool through one write, so a test
// that seeds a spool exercises what a requeue exercises.
func (s *Sandbox) SeedRecord(kind envelope.Kind, sessionID, projectIDHash string, at time.Time) string {
	s.t.Helper()
	data, id := seedRecord(s.t, kind, sessionID, projectIDHash, at)
	if err := s.openSpool().WriteRecord(data); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// SeedSegment stores one segment of a session's own file, as a reading
// run would, and returns the record id it was stored under.
func (s *Sandbox) SeedSegment(sessionID, projectIDHash string, at time.Time) string {
	s.t.Helper()
	return s.SeedRecord(envelope.KindSegment, sessionID, projectIDHash, at)
}

// SeedMetaSnapshot stores one snapshot of a sub-agent's metadata file
// and returns the record id it was stored under.
func (s *Sandbox) SeedMetaSnapshot(sessionID, projectIDHash string, at time.Time) string {
	s.t.Helper()
	return s.SeedRecord(envelope.KindMetaSnapshot, sessionID, projectIDHash, at)
}

// SeedGitSnapshot stores one observation of a project's repository, as
// a session hook would, and returns the record id it was stored under.
func (s *Sandbox) SeedGitSnapshot(sessionID, projectIDHash string, at time.Time) string {
	s.t.Helper()
	return s.SeedRecord(envelope.KindGitSnapshot, sessionID, projectIDHash, at)
}

// seedRecord builds one record of the given kind: the content each kind
// carries is the one thing a seeder cannot share.
func seedRecord(t *testing.T, kind envelope.Kind, sessionID, projectIDHash string, at time.Time) ([]byte, string) {
	t.Helper()
	capture := seedCapture(projectIDHash, at)
	var data []byte
	var err error
	switch kind {
	case envelope.KindSegment:
		data, err = envelope.NewSegment(sessionID, "", 0, capture,
			`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n").Bytes()
	case envelope.KindMetaSnapshot:
		var snap envelope.MetaSnapshot
		if snap, err = envelope.NewMetaSnapshot(sessionID, "subagents/agent-0000.meta.json",
			capture, []byte(`{"agentId":"0000"}`)); err == nil {
			data, err = snap.Bytes()
		}
	case envelope.KindGitSnapshot:
		data, err = envelope.NewGitSnapshot(sessionID, "SessionStart", envelope.TriggerSessionStart,
			capture, "main", "d0cf90f327430f11f8a68493a58f402fa11d7c9e",
			envelope.CommitOrNone(""), envelope.CommitOrNone(""), nil).Bytes()
	default:
		t.Fatalf("no seed for a record of kind %s/%s", kind.Source, kind.RecordKind)
	}
	if err != nil {
		t.Fatal(err)
	}
	header, err := envelope.ReadHeader(data)
	if err != nil {
		t.Fatal(err)
	}
	return data, header.RecordID
}

// Records reports every record of the second slot currently stored.
func (s *Sandbox) Records() []Record {
	s.t.Helper()
	sp, err := spool.Open(s.layout.SpoolDir(), 0)
	if err != nil {
		s.t.Fatal(err)
	}
	var stored []Record
	if err := sp.EachRecord(func(r Record) error {
		stored = append(stored, r)
		return nil
	}); err != nil {
		s.t.Fatal(err)
	}
	return stored
}

// GitSnapshots is every observation of a repository the spool holds,
// read back from its own bytes and in the order the observations were
// made. The spool addresses a record by an id that is a digest, so the
// order is restored here from what each record states about itself.
func (s *Sandbox) GitSnapshots() []envelope.GitSnapshot {
	s.t.Helper()
	var found []envelope.GitSnapshot
	for _, r := range s.Records() {
		if r.Kind != envelope.KindGitSnapshot.RecordKind {
			continue
		}
		snap, err := envelope.ParseGitSnapshot(r.Raw)
		if err != nil {
			s.t.Fatalf("stored observation does not read back: %v", err)
		}
		found = append(found, snap)
	}
	slices.SortStableFunc(found, func(a, b envelope.GitSnapshot) int {
		return cmp.Compare(a.Capture.Timestamp, b.Capture.Timestamp)
	})
	return found
}

// SessionsHeld counts what the spool still holds for each session,
// rawcalls and records together.
func (s *Sandbox) SessionsHeld() map[string]int {
	s.t.Helper()
	held := map[string]int{}
	for _, r := range s.Rawcalls() {
		if id, ok := spool.SessionIDFromUserID(r.SessionKey); ok {
			held[id]++
		}
	}
	for _, r := range s.Records() {
		held[r.SessionID]++
	}
	return held
}

// RequestBodyOfSession is a request body that names sessionID the way
// a client names the session it runs in, so a rawcall seeded with it
// belongs to that session.
func RequestBodyOfSession(t *testing.T, sessionID string) []byte {
	t.Helper()
	userID, err := json.Marshal(map[string]string{"device_id": "d", "account_uuid": "a", "session_id": sessionID})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"model":    "claude-fable-5",
		"metadata": map[string]string{"user_id": string(userID)},
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (s *Sandbox) openSpool() *spool.Spool {
	s.t.Helper()
	sp, err := spool.Create(s.layout.SpoolDir(), 0)
	if err != nil {
		s.t.Fatal(err)
	}
	return sp
}

func seedCapture(projectIDHash string, at time.Time) envelope.Capture {
	return envelope.Capture{
		ClientVersion: seedClientVersion,
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		ProjectIDHash: projectIDHash,
		Injection:     envelope.InjectionProxy,
	}
}
