package fakeplatform_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func TestStubbedEndpointServesJSON(t *testing.T) {
	s := fakeplatform.New(t)
	s.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]any{"ack": true}))

	resp, err := s.HTTP.Client().Post(s.URL()+"/v1/batches", "application/octet-stream", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Ack bool }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !body.Ack {
		t.Errorf("response = %d ack=%v, want 200 ack=true", resp.StatusCode, body.Ack)
	}
}

func TestStubsConsumedFIFOWithStickyLast(t *testing.T) {
	s := fakeplatform.New(t)
	s.Stub("GET", "/v1/handshake", fakeplatform.JSON(500, map[string]string{"error": "transient"}))
	s.Stub("GET", "/v1/handshake", fakeplatform.JSON(200, map[string]string{"status": "ok"}))

	wantStatuses := []int{500, 200, 200}
	for i, want := range wantStatuses {
		resp, err := s.HTTP.Client().Get(s.URL() + "/v1/handshake")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("call %d status = %d, want %d", i, resp.StatusCode, want)
		}
	}
}

func TestUnstubbedEndpointFailsLoudly(t *testing.T) {
	s := fakeplatform.New(t)
	resp, err := s.HTTP.Client().Get(s.URL() + "/v1/unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 590 {
		t.Errorf("status = %d, want 590", resp.StatusCode)
	}
}

func TestRecordsRequests(t *testing.T) {
	s := fakeplatform.New(t)
	s.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]bool{"ack": true}))

	req, err := http.NewRequest(http.MethodPost, s.URL()+"/v1/batches?batch_id=b1", bytes.NewReader([]byte{0x28, 0xb5, 0x2f, 0xfd}))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer device-token-fake")
	resp, err := s.HTTP.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	got := reqs[0]
	if got.URL != "/v1/batches?batch_id=b1" {
		t.Errorf("URL = %q, want /v1/batches?batch_id=b1", got.URL)
	}
	if !bytes.Equal(got.Body, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Errorf("body = %x, want 28b52ffd", got.Body)
	}
	if got.Header.Get("Authorization") != "Bearer device-token-fake" {
		t.Errorf("Authorization = %q, want recorded verbatim", got.Header.Get("Authorization"))
	}
}

func TestRecordIDsBySourceGroupsTheIndexBySource(t *testing.T) {
	at := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	seg := envelope.NewSegment("sess-1", "", 0, fixtureCapture(at), "{}\n")
	snap, err := envelope.NewMetaSnapshot("sess-1", "subagents/agent-x.meta.json", fixtureCapture(at), []byte(`{"agentId":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	segBytes, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	snapBytes, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	in := spool.Entries{
		fixtureRawcall(t, "msg_1", at), fixtureRawcall(t, "msg_2", at.Add(time.Minute)),
		storedRecord(t, envelope.KindSegment, seg.RecordID, segBytes),
		storedRecord(t, envelope.KindMetaSnapshot, snap.RecordID, snapBytes),
	}
	b, refused, err := batch.Build("b-1", at, "test", in, batch.Run{})
	if err != nil || len(refused) != 0 {
		t.Fatalf("Build: %v, refused %+v", err, refused)
	}

	groups, err := fakeplatform.RecordIDsBySource(b.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(groups["proxy"], ","); got != "msg_1,msg_2" {
		t.Errorf("proxy = %q", got)
	}
	if got, want := strings.Join(groups["transcript"], ","), seg.RecordID+","+snap.RecordID; got != want {
		t.Errorf("transcript = %q, want %q", got, want)
	}
	if len(groups) != 2 {
		t.Errorf("groups = %v", groups)
	}
}

func fixtureCapture(at time.Time) envelope.TranscriptCapture {
	return envelope.TranscriptCapture{
		ClientVersion: "test",
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		ProjectIDHash: "hash-p1",
		Injection:     envelope.InjectionProxy,
	}
}

func fixtureRawcall(t *testing.T, id string, at time.Time) spool.Entry {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider:          "anthropic",
		Endpoint:          "/v1/messages",
		HTTPStatus:        200,
		ClientVersion:     "test",
		ProjectIDHash:     "hash-p1",
		At:                at,
		Upstream:          "https://api.anthropic.com",
		OfficialUpstream:  "https://api.anthropic.com",
		Request:           []byte(`{"model":"m"}`),
		RequestComplete:   true,
		Response:          []byte(`{"type":"message"}`),
		ResponseComplete:  true,
		ContentType:       "application/json",
		UpstreamRequestID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spool.Entry{Kind: envelope.KindRawcall, ID: env.RequestID(), Timestamp: at, Raw: env.Bytes()}
}

func storedRecord(t *testing.T, kind envelope.Kind, id string, raw []byte) spool.Entry {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("record %s has no bytes", id)
	}
	return spool.Entry{Kind: kind, ID: id, Raw: raw}
}

func TestRecordIDsBySourceRefusesASchemaVersion1Index(t *testing.T) {
	v1 := []byte(`{"schema_version":"1","batch_id":"b","records":[{"request_id":"msg_1","offset":0,"size":1}]}`)
	if _, err := fakeplatform.RecordIDsBySource(v1); err == nil {
		t.Error("a schema_version 1 index was grouped")
	}
}
