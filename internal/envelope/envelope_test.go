package envelope_test

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

var update = flag.Bool("update", false, "rewrite golden files")

func assembled(obj string) envelope.Assembler {
	return envelope.Assembler{
		Rules:    "1",
		Assemble: func([]byte) (json.RawMessage, error) { return json.RawMessage(obj), nil },
	}
}

// quoted is how a body that is not valid JSON is stored: as a JSON
// string holding the raw bytes.
func quoted(raw string) string {
	encoded, err := json.Marshal(raw)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func degraded() envelope.Assembler {
	return envelope.Assembler{
		Rules: "1",
		Assemble: func([]byte) (json.RawMessage, error) {
			return nil, errors.New("stream cannot be represented faithfully")
		},
	}
}

func TestRecordMatchesGoldenSchemaV1(t *testing.T) {
	env, err := envelope.Record(envelope.Observation{
		Provider:         "anthropic",
		Endpoint:         "/v1/messages",
		HTTPStatus:       200,
		ClientVersion:    "1.2.3",
		ProjectIDHash:    "0011aabb",
		At:               time.Date(2026, 8, 1, 12, 30, 45, 123456789, time.UTC),
		Upstream:         "https://api.anthropic.com",
		OfficialUpstream: "https://api.anthropic.com",
		Request:          []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}]}`),
		RequestComplete:  true,
		Response:         []byte("event: message_stop\ndata: {}\n\n"),
		ResponseComplete: true,
		ContentType:      "text/event-stream",
		Assembler:        assembled(`{"id":"msg_01ABCDEF","model":"claude-fable-5","usage":{"output_tokens":9}}`),
		Hints: envelope.FormatHints{
			AnthropicVersion: "2023-06-01",
			AnthropicBeta:    []string{"beta-a", "beta-b"},
		},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := env.Bytes()
	golden := filepath.Join("testdata", "envelope_v1.json")
	if *update {
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	if string(got) != strings.TrimSuffix(string(want), "\n") {
		t.Errorf("serialized rawcall diverged from golden file\ngot:  %s\nwant: %s", got, want)
	}
}

func TestGarbledClassification(t *testing.T) {
	const validJSON = `{"id":"msg_ok"}`
	const stream = "event: message_stop\ndata: {}\n\n"

	tests := []struct {
		name        string
		obs         envelope.Observation
		wantGarbled bool
		wantBy      string
		wantStored  string
	}{
		{
			name: "complete json response is stored verbatim",
			obs: envelope.Observation{
				Response: []byte(validJSON), ResponseComplete: true, ContentType: "application/json",
			},
			wantBy:     "none",
			wantStored: validJSON,
		},
		{
			name: "an encoding the transport could not decode is kept raw",
			obs: envelope.Observation{
				Response: []byte(validJSON), ResponseComplete: true,
				ContentType: "application/json", ContentEncoding: "br",
			},
			wantGarbled: true,
			wantBy:      "none",
			wantStored:  quoted(validJSON),
		},
		{
			name: "a complete stream is assembled",
			obs: envelope.Observation{
				Response: []byte(stream), ResponseComplete: true,
				ContentType: "text/event-stream", Assembler: assembled(validJSON),
			},
			wantBy:     "client",
			wantStored: validJSON,
		},
		{
			name: "a malformed stream is kept as raw text",
			obs: envelope.Observation{
				Response: []byte(stream), ResponseComplete: true,
				ContentType: "text/event-stream", Assembler: degraded(),
			},
			wantGarbled: true,
			wantBy:      "none",
			wantStored:  quoted(stream),
		},
		{
			name: "a truncated stream is never assembled",
			obs: envelope.Observation{
				Response: []byte(stream), ResponseComplete: false,
				ContentType: "text/event-stream", Assembler: assembled(validJSON),
			},
			wantGarbled: true,
			wantBy:      "none",
			wantStored:  quoted(stream),
		},
		{
			name: "a non-JSON response is kept as raw text",
			obs: envelope.Observation{
				Response: []byte("upstream said no"), ResponseComplete: true, ContentType: "text/plain",
			},
			wantGarbled: true,
			wantBy:      "none",
			wantStored:  quoted("upstream said no"),
		},
		{
			name: "a truncated JSON response is kept as raw text",
			obs: envelope.Observation{
				Response: []byte(`{"id":"msg_`), ResponseComplete: false, ContentType: "application/json",
			},
			wantGarbled: true,
			wantBy:      "none",
			wantStored:  quoted(`{"id":"msg_`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.obs.Request = []byte(`{"model":"m"}`)
			tt.obs.RequestComplete = true
			env, err := envelope.Record(tt.obs)
			if err != nil {
				t.Fatal(err)
			}
			if env.Garbled() != tt.wantGarbled {
				t.Errorf("Garbled() = %v, want %v", env.Garbled(), tt.wantGarbled)
			}
			if env.AssembledBy() != tt.wantBy {
				t.Errorf("AssembledBy() = %q, want %q", env.AssembledBy(), tt.wantBy)
			}
			if string(env.Response()) != tt.wantStored {
				t.Errorf("Response() = %s, want %s", env.Response(), tt.wantStored)
			}
		})
	}
}

func TestTruncatedRequestGarblesTheRecord(t *testing.T) {
	env, err := envelope.Record(envelope.Observation{
		Request:          []byte(`{"model":"m`),
		RequestComplete:  false,
		Response:         []byte(`{"id":"msg_ok"}`),
		ResponseComplete: true,
		ContentType:      "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !env.Garbled() {
		t.Error("a truncated request left the record unmarked")
	}
	if string(env.Request()) != quoted(`{"model":"m`) {
		t.Errorf("Request() = %s, want the raw bytes as a JSON string", env.Request())
	}
}

func TestRequestIDPolicy(t *testing.T) {
	base := envelope.Observation{
		Request: []byte(`{}`), RequestComplete: true, ContentType: "application/json",
		ResponseComplete: true,
	}

	t.Run("the response id wins", func(t *testing.T) {
		obs := base
		obs.Response = []byte(`{"id":"msg_from_body"}`)
		obs.UpstreamRequestID = "req_from_header"
		env, _ := envelope.Record(obs)
		if env.RequestID() != "msg_from_body" {
			t.Errorf("RequestID() = %q", env.RequestID())
		}
	})

	t.Run("the upstream id is the fallback", func(t *testing.T) {
		obs := base
		obs.Response = []byte(`{"model":"m"}`)
		obs.UpstreamRequestID = "req_from_header"
		env, _ := envelope.Record(obs)
		if env.RequestID() != "req_from_header" {
			t.Errorf("RequestID() = %q", env.RequestID())
		}
	})

	t.Run("an unusable id is replaced by a marked local one", func(t *testing.T) {
		obs := base
		obs.Response = []byte(`{"id":"../../escape"}`)
		env, _ := envelope.Record(obs)
		if !strings.HasPrefix(env.RequestID(), "local-") {
			t.Errorf("RequestID() = %q, want a local- prefixed id", env.RequestID())
		}
		other, _ := envelope.Record(obs)
		if env.RequestID() == other.RequestID() {
			t.Errorf("two generated ids collided: %q", env.RequestID())
		}
	})

	t.Run("no id at all still yields a storable one", func(t *testing.T) {
		obs := base
		obs.Response = []byte(`{"model":"m"}`)
		env, _ := envelope.Record(obs)
		if !strings.HasPrefix(env.RequestID(), "local-") {
			t.Errorf("RequestID() = %q", env.RequestID())
		}
	})
}

func TestOriginIsNeverDefaultedToOfficial(t *testing.T) {
	tests := []struct {
		upstream, official, want string
	}{
		{"https://api.anthropic.com", "https://api.anthropic.com", "official"},
		{"https://api.anthropic.com/", "https://api.anthropic.com", "official"},
		{"https://relay.example.com", "https://api.anthropic.com", "third_party"},
		{"", "https://api.anthropic.com", "third_party"},
	}
	for _, tt := range tests {
		if got := envelope.Origin(tt.upstream, tt.official); got != tt.want {
			t.Errorf("Origin(%q, %q) = %q, want %q", tt.upstream, tt.official, got, tt.want)
		}
	}
}

func TestSessionKeyIsCopiedVerbatimOrEmpty(t *testing.T) {
	withKey, _ := envelope.Record(envelope.Observation{
		Request:         []byte(`{"metadata":{"user_id":"session-key-1"}}`),
		RequestComplete: true,
	})
	if withKey.SessionKey() != "session-key-1" {
		t.Errorf("SessionKey() = %q", withKey.SessionKey())
	}
	without, _ := envelope.Record(envelope.Observation{
		Request:         []byte(`{"model":"m"}`),
		RequestComplete: true,
	})
	if without.SessionKey() != "" {
		t.Errorf("SessionKey() = %q, want empty", without.SessionKey())
	}
}

func TestParseRoundTripsAStoredRecord(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 30, 45, 123456789, time.UTC)
	written, err := envelope.Record(envelope.Observation{
		Provider: "anthropic", Endpoint: "/v1/messages", HTTPStatus: 200,
		ClientVersion: "1.2.3", ProjectIDHash: "0011aabb", At: at,
		Upstream: "https://relay.example.com", OfficialUpstream: "https://api.anthropic.com",
		Request:     []byte(`{"metadata":{"user_id":"sk-1"}}`),
		Response:    []byte(`{"id":"msg_round"}`),
		ContentType: "application/json", RequestComplete: true, ResponseComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := envelope.Parse(written.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if read.RequestID() != "msg_round" || read.SessionKey() != "sk-1" {
		t.Errorf("identity = %q/%q", read.RequestID(), read.SessionKey())
	}
	if !read.Timestamp().Equal(at) {
		t.Errorf("Timestamp() = %v, want %v", read.Timestamp(), at)
	}
	if read.Endpoint() != "/v1/messages" || read.HTTPStatus() != 200 {
		t.Errorf("endpoint/status = %q/%d", read.Endpoint(), read.HTTPStatus())
	}
	if read.ProjectIDHash() != "0011aabb" || read.UpstreamOrigin() != "third_party" {
		t.Errorf("project/origin = %q/%q", read.ProjectIDHash(), read.UpstreamOrigin())
	}
}

func TestParseRejectsUnreadableOrForeignRecords(t *testing.T) {
	if _, err := envelope.Parse([]byte("not json")); err == nil {
		t.Error("unreadable bytes parsed")
	}
	foreign, _ := json.Marshal(map[string]any{"schema_version": "99"})
	if _, err := envelope.Parse(foreign); err == nil {
		t.Error("a future schema version parsed")
	}
}

func TestProjectIDHashOfReadsPartialRecords(t *testing.T) {
	partial := []byte(`{"capture":{"project_id_hash":"0011aabb"},"response":`)
	if _, ok := envelope.ProjectIDHashOf(partial); ok {
		t.Error("truncated JSON yielded a hash")
	}
	minimal := []byte(`{"capture":{"project_id_hash":"0011aabb"}}`)
	hash, ok := envelope.ProjectIDHashOf(minimal)
	if !ok || hash != "0011aabb" {
		t.Errorf("ProjectIDHashOf = %q, %v", hash, ok)
	}
	if _, ok := envelope.ProjectIDHashOf([]byte(`{"capture":{}}`)); ok {
		t.Error("a record without a hash reported one")
	}
}

func TestTimestampIsRFC3339NanoUTC(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	env, err := envelope.Record(envelope.Observation{
		At: time.Date(2026, 8, 1, 20, 0, 0, 5, loc),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env.Bytes()), `"timestamp":"2026-08-01T12:00:00.000000005Z"`) {
		t.Errorf("serialized timestamp = %s", env.Bytes())
	}
}

func TestArbitraryBytesStayValidJSON(t *testing.T) {
	raw := []byte("event: message_start\ndata: {\"partial\n")
	env, err := envelope.Record(envelope.Observation{Response: raw, ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded string
	if err := json.Unmarshal(env.Response(), &decoded); err != nil {
		t.Fatalf("stored body is not valid JSON: %v", err)
	}
	if decoded != string(raw) {
		t.Errorf("round trip changed content: %q", decoded)
	}
}
