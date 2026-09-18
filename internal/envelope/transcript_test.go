package envelope_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

const (
	fixtureSessionID = "1b2c3d4e-5f60-4718-9a0b-c1d2e3f4a5b6"
	fixtureAgentFile = "subagents/agent-a1b2c3d4"
	fixtureMetaJSON  = `{"agentId":"a1b2c3d4","toolUseId":"toolu_01FIXTURE","spawnDepth":1,"name":"Explore","createdAt":"2026-09-10T09:00:00.000000000Z"}`
)

func fixtureCapture() envelope.Capture {
	return envelope.Capture{
		ClientVersion: "0.2.0",
		Timestamp:     "2026-09-10T09:00:00.000000000Z",
		ProjectIDHash: "a4935b31d2ff72636fb53f77bb80a37fe44f9e113820330ebae207b70108a58e",
		Injection:     envelope.InjectionProxy,
	}
}

func TestSegmentRecordIDIsDeterminedByIdentityAlone(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		index int
		want  string
	}{
		{"main file first segment", "", 0, "seg_352333f5dc638f1374b930d6a0ffa419"},
		{"main file second segment", "", 1, "seg_a9f0311e5221d0314d6b565ed274d98a"},
		{"main file third segment", "", 2, "seg_36d287b4221b79dd210626e295d35d8d"},
		{"agent file first segment", fixtureAgentFile + ".jsonl", 0, "seg_03d83a70763cd06406201a1be2abfabe"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := envelope.SegmentRecordID(fixtureSessionID, tc.file, tc.index); got != tc.want {
				t.Errorf("SegmentRecordID = %q, want %q", got, tc.want)
			}
			again := envelope.NewSegment(fixtureSessionID, tc.file, tc.index, fixtureCapture(), "{}\n")
			if again.RecordID != tc.want {
				t.Errorf("NewSegment named the record %q, want %q", again.RecordID, tc.want)
			}
		})
	}
}

func TestMetaSnapshotRecordIDChangesWithContentNotWithItsSpelling(t *testing.T) {
	const want = "meta_31c2ffedaf511eff8c8cca0cf7854868"
	file := fixtureAgentFile + ".meta.json"
	got, err := envelope.MetaSnapshotRecordID(fixtureSessionID, file, []byte(fixtureMetaJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("MetaSnapshotRecordID = %q, want %q", got, want)
	}

	reordered := `{ "name": "Explore", "createdAt": "2026-09-10T09:00:00.000000000Z",
		"spawnDepth": 1, "toolUseId": "toolu_01FIXTURE", "agentId": "a1b2c3d4" }`
	same, err := envelope.MetaSnapshotRecordID(fixtureSessionID, file, []byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if same != want {
		t.Errorf("reordered keys and whitespace changed the id to %q", same)
	}

	changed := strings.Replace(fixtureMetaJSON, `"spawnDepth":1`, `"spawnDepth":2`, 1)
	other, err := envelope.MetaSnapshotRecordID(fixtureSessionID, file, []byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	if other == want {
		t.Error("changed content kept the same id")
	}
	if !strings.HasPrefix(other, "meta_") || len(other) != len(want) {
		t.Errorf("id shape = %q", other)
	}
}

func TestMetaSnapshotRecordIDRefusesNonJSONContent(t *testing.T) {
	for _, content := range []string{"", "not json", `{"a":1} trailing`} {
		if _, err := envelope.MetaSnapshotRecordID(fixtureSessionID, "f", []byte(content)); err == nil {
			t.Errorf("content %q was named", content)
		}
	}
}

func TestSegmentRoundTripsByteForByte(t *testing.T) {
	seg := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(),
		"{\"type\":\"user\",\"cwd\":\"[REDACTED_PATH]\",\"text\":\"a<b>&c\"}\n")
	data, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := `{"schema_version":"3","source":"transcript","record_kind":"segment","record_id":"seg_352333f5dc638f1374b930d6a0ffa419","session_id":"` + fixtureSessionID + `","file":"","segment_index":0,"capture":{"client_version":"0.2.0","timestamp":"2026-09-10T09:00:00.000000000Z","project_id_hash":"a4935b31d2ff72636fb53f77bb80a37fe44f9e113820330ebae207b70108a58e","injection":"proxy"},"lines":"`
	if !strings.HasPrefix(string(data), wantPrefix) {
		t.Fatalf("serialized segment = %s", data)
	}
	if strings.Contains(string(data), "\\u003c") {
		t.Errorf("angle brackets were HTML-escaped: %s", data)
	}
	read, err := envelope.ParseSegment(data)
	if err != nil {
		t.Fatal(err)
	}
	if read != seg {
		t.Errorf("round trip changed the record:\n got %+v\nwant %+v", read, seg)
	}
	again, err := read.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("second serialization differs:\n got %s\nwant %s", again, data)
	}
}

func TestSegmentSubpathIsAbsentNotEmptyAtProjectRoot(t *testing.T) {
	root := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(), "{}\n")
	data, _ := root.Bytes()
	if strings.Contains(string(data), "project_subpath") {
		t.Error("a root session carried project_subpath")
	}
	c := fixtureCapture()
	c.ProjectSubpath = "apps/api"
	c.Injection = envelope.InjectionTailOnly
	nested := envelope.NewSegment(fixtureSessionID, "", 0, c, "{}\n")
	data, _ = nested.Bytes()
	if !strings.Contains(string(data), `"project_subpath":"apps/api","injection":"tail_only"`) {
		t.Errorf("nested session capture = %s", data)
	}
}

func TestMetaSnapshotKeepsContentBytesVerbatim(t *testing.T) {
	m, err := envelope.NewMetaSnapshot(fixtureSessionID, fixtureAgentFile+".meta.json", fixtureCapture(), []byte(fixtureMetaJSON))
	if err != nil {
		t.Fatal(err)
	}
	data, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), `,"content":`+fixtureMetaJSON+`}`) {
		t.Errorf("content was re-serialized: %s", data)
	}
	read, err := envelope.ParseMetaSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if read.RecordID != "meta_31c2ffedaf511eff8c8cca0cf7854868" || string(read.Content) != fixtureMetaJSON {
		t.Errorf("read back = %+v", read)
	}
}

func TestTranscriptParsersRefuseEachOthersRecordsAndRawcalls(t *testing.T) {
	seg, _ := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(), "{}\n").Bytes()
	snap, _ := envelope.NewMetaSnapshot(fixtureSessionID, "f.meta.json", fixtureCapture(), []byte(`{}`))
	snapData, _ := snap.Bytes()
	rawcall, _ := json.Marshal(map[string]any{"schema_version": "1", "source": "proxy"})
	future, _ := json.Marshal(map[string]any{"schema_version": "9", "source": "transcript", "record_kind": "segment"})

	if _, err := envelope.ParseSegment(snapData); err == nil {
		t.Error("a snapshot parsed as a segment")
	}
	if _, err := envelope.ParseMetaSnapshot(seg); err == nil {
		t.Error("a segment parsed as a snapshot")
	}
	for _, data := range [][]byte{rawcall, future, []byte("not json")} {
		if _, err := envelope.ParseSegment(data); err == nil {
			t.Errorf("%s parsed as a segment", data)
		}
		if _, err := envelope.ParseMetaSnapshot(data); err == nil {
			t.Errorf("%s parsed as a snapshot", data)
		}
	}
}

func TestKindOfReadsTheSelfDeclarationOfEveryRecord(t *testing.T) {
	seg, _ := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(), "{}\n").Bytes()
	snap, _ := envelope.NewMetaSnapshot(fixtureSessionID, "f.meta.json", fixtureCapture(), []byte(`{}`))
	snapData, _ := snap.Bytes()
	rawcall, err := envelope.Record(envelope.Observation{
		Response: []byte(`{"id":"msg_1"}`), ResponseComplete: true, ContentType: "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		want envelope.Kind
	}{
		{"rawcall", rawcall.Bytes(), envelope.KindRawcall},
		{"segment", seg, envelope.KindSegment},
		{"meta snapshot", snapData, envelope.KindMetaSnapshot},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := envelope.KindOf(tc.data)
			if err != nil || got != tc.want {
				t.Errorf("KindOf = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	if _, err := envelope.KindOf([]byte("{")); err == nil {
		t.Error("unreadable bytes reported a kind")
	}
}

func TestUpstreamRequestIDIsStoredWhenObservedAndAbsentOtherwise(t *testing.T) {
	withHeader, err := envelope.Record(envelope.Observation{
		Response: []byte(`{"id":"msg_1"}`), ResponseComplete: true, ContentType: "application/json",
		UpstreamRequestID: "req_011ABC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if withHeader.RequestID() != "msg_1" || withHeader.UpstreamRequestID() != "req_011ABC" {
		t.Errorf("ids = %q/%q", withHeader.RequestID(), withHeader.UpstreamRequestID())
	}
	if !strings.Contains(string(withHeader.Bytes()), `"project_id_hash":"","upstream_request_id":"req_011ABC","upstream_origin":`) {
		t.Errorf("serialized capture = %s", withHeader.Bytes())
	}
	read, err := envelope.Parse(withHeader.Bytes())
	if err != nil || read.UpstreamRequestID() != "req_011ABC" {
		t.Errorf("read back upstream id = %q, %v", read.UpstreamRequestID(), err)
	}

	without, err := envelope.Record(envelope.Observation{
		Response: []byte(`{"id":"msg_2"}`), ResponseComplete: true, ContentType: "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without.Bytes()), "upstream_request_id") {
		t.Errorf("an unobserved upstream id was written: %s", without.Bytes())
	}
}
