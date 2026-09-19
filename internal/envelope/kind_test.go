package envelope_test

import (
	"slices"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

func rawcallBytes(t *testing.T) []byte {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider:         "anthropic",
		Endpoint:         "/v1/messages",
		HTTPStatus:       200,
		ClientVersion:    "1.2.3",
		ProjectIDHash:    "0011aabb",
		At:               time.Date(2026, 8, 1, 12, 30, 45, 0, time.UTC),
		Upstream:         "https://api.anthropic.com",
		OfficialUpstream: "https://api.anthropic.com",
		Request:          []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}]}`),
		RequestComplete:  true,
		Response:         []byte(`{"id":"msg_01ABCDEF"}`),
		ResponseComplete: true,
		ContentType:      "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	return env.Bytes()
}

func segmentBytes(t *testing.T) []byte {
	t.Helper()
	data, err := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(), "{}\n").Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func metaSnapshotBytes(t *testing.T) []byte {
	t.Helper()
	snap, err := envelope.NewMetaSnapshot(fixtureSessionID, "subagents/agent-0000.meta.json", fixtureCapture(), []byte(`{"agentId":"0000"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func gitSnapshotBytes(t *testing.T) []byte {
	t.Helper()
	snap := envelope.NewGitSnapshot(fixtureSessionID, "SessionStart", envelope.TriggerSessionStart,
		fixtureCapture(), "main", "d0cf90f327430f11f8a68493a58f402fa11d7c9e",
		envelope.CommitOrNone(""), envelope.CommitOrNone(""), nil)
	data, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSessionIDIsReadFromEveryKindThatDeclaresOne(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		want  string
		found bool
	}{
		{name: "rawcall", data: rawcallBytes(t)},
		{name: "segment", data: segmentBytes(t), want: fixtureSessionID, found: true},
		{name: "meta snapshot", data: metaSnapshotBytes(t), want: fixtureSessionID, found: true},
		{name: "git snapshot", data: gitSnapshotBytes(t), want: fixtureSessionID, found: true},
		{name: "kind this client does not store", data: []byte(`{"source":"elsewhere","record_kind":"other","session_id":"` + fixtureSessionID + `"}`), want: fixtureSessionID, found: true},
		{name: "not a record at all", data: []byte("{")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := envelope.SessionIDOf(tc.data)
			if got != tc.want || ok != tc.found {
				t.Errorf("SessionIDOf = %q, %v; want %q, %v", got, ok, tc.want, tc.found)
			}
		})
	}
}

func TestEveryDeclaredKindIsInTheKindTable(t *testing.T) {
	for _, tc := range []struct {
		kind     envelope.Kind
		countKey string
	}{
		{envelope.KindRawcall, "rawcalls"},
		{envelope.KindSegment, "segments"},
		{envelope.KindMetaSnapshot, "snapshots"},
		{envelope.KindGitSnapshot, "git_snapshots"},
	} {
		t.Run(tc.countKey, func(t *testing.T) {
			if !slices.Contains(envelope.Kinds(), tc.kind) {
				t.Errorf("Kinds() does not list %s/%s", tc.kind.Source, tc.kind.RecordKind)
			}
			if got := tc.kind.CountKey(); got != tc.countKey {
				t.Errorf("CountKey() = %q, want %q", got, tc.countKey)
			}
		})
	}
	if got := (envelope.Kind{Source: "elsewhere", RecordKind: "other"}).CountKey(); got != "" {
		t.Errorf("CountKey() of a kind this client does not store = %q, want empty", got)
	}
}

func TestReadHeaderRefusesBytesThatDoNotReadBackAsTheKindTheyDeclare(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "kind this client does not store", data: []byte(`{"schema_version":"3","source":"elsewhere","record_kind":"other"}`)},
		{name: "segment that does not parse", data: []byte(`{"schema_version":"3","source":"transcript","record_kind":"segment","capture":[]}`)},
		{name: "git snapshot from a later schema", data: []byte(`{"schema_version":"9","source":"hook","record_kind":"git_snapshot"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := envelope.ReadHeader(tc.data); err == nil {
				t.Error("ReadHeader accepted a record nothing can interpret")
			}
		})
	}
}

func TestReadHeaderStatesWhatEverySecondSlotRecordDeclares(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		kind envelope.Kind
	}{
		{name: "segment", data: segmentBytes(t), kind: envelope.KindSegment},
		{name: "meta snapshot", data: metaSnapshotBytes(t), kind: envelope.KindMetaSnapshot},
		{name: "git snapshot", data: gitSnapshotBytes(t), kind: envelope.KindGitSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header, err := envelope.ReadHeader(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if header.Kind != tc.kind {
				t.Errorf("kind = %s/%s, want %s/%s", header.Kind.Source, header.Kind.RecordKind, tc.kind.Source, tc.kind.RecordKind)
			}
			if header.SessionID != fixtureSessionID || header.RecordID == "" {
				t.Errorf("record %q of session %q, want a record id and session %q", header.RecordID, header.SessionID, fixtureSessionID)
			}
			if header.Capture != fixtureCapture() {
				t.Errorf("capture = %+v, want %+v", header.Capture, fixtureCapture())
			}
		})
	}
}
