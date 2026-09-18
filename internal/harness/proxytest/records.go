package proxytest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// Record is one stored segment or snapshot, in spool's own type.
type Record = spool.Record

// seedClientVersion is the build a seeded record states it came from.
const seedClientVersion = "seed-build"

// SeedSegment stores one segment of a session's own file, as a reading
// run would, and returns the record id it was stored under.
func (s *Sandbox) SeedSegment(sessionID, projectIDHash string, at time.Time) string {
	s.t.Helper()
	seg := envelope.NewSegment(sessionID, "", 0, seedCapture(projectIDHash, at),
		`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n")
	sp := s.openSpool()
	if err := sp.WriteSegment(seg); err != nil {
		s.t.Fatal(err)
	}
	return seg.RecordID
}

// SeedMetaSnapshot stores one snapshot of a sub-agent's metadata file
// and returns the record id it was stored under.
func (s *Sandbox) SeedMetaSnapshot(sessionID, projectIDHash string, at time.Time) string {
	s.t.Helper()
	snap, err := envelope.NewMetaSnapshot(sessionID, "subagents/agent-0000.meta.json",
		seedCapture(projectIDHash, at), []byte(`{"agentId":"0000"}`))
	if err != nil {
		s.t.Fatal(err)
	}
	sp := s.openSpool()
	if err := sp.WriteMetaSnapshot(snap); err != nil {
		s.t.Fatal(err)
	}
	return snap.RecordID
}

// SeedGitSnapshot stores one observation of a project's repository, as
// a session hook would, and returns the record id it was stored under.
func (s *Sandbox) SeedGitSnapshot(sessionID, projectIDHash string, at time.Time) string {
	s.t.Helper()
	snap := envelope.NewGitSnapshot(sessionID, "SessionStart", envelope.TriggerSessionStart,
		seedCapture(projectIDHash, at), "main", "d0cf90f327430f11f8a68493a58f402fa11d7c9e",
		envelope.CommitOrNone(""), envelope.CommitOrNone(""), nil)
	sp := s.openSpool()
	if err := sp.WriteGitSnapshot(snap); err != nil {
		s.t.Fatal(err)
	}
	return snap.RecordID
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
