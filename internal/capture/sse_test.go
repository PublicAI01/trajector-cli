package capture_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/capture"
)

func ev(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return v
}

func TestAssembleEquivalentNonStreamingObject(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{
			name: "text response with usage merge",
			stream: ev("message_start", `{"type":"message_start","message":{"id":"msg_017","type":"message","role":"assistant","model":"claude-fable-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
				ev("ping", `{"type":"ping"}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":12}}`) +
				ev("message_stop", `{"type":"message_stop"}`),
			want: `{"id":"msg_017","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"text","text":"Hello world"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":12}}`,
		},
		{
			name: "thinking block keeps signature fragments verbatim",
			stream: ev("message_start", `{"type":"message_start","message":{"id":"msg_018","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCg"}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"kIkl=="}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":1}`) +
				ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`) +
				ev("message_stop", `{"type":"message_stop"}`),
			want: `{"id":"msg_018","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"thinking","thinking":"Let me think","signature":"EqQBCgkIkl=="},{"type":"text","text":"Done"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":7}}`,
		},
		{
			name: "tool use input assembled from fragments",
			stream: ev("message_start", `{"type":"message_start","message":{"id":"msg_019","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\",\"population\":9007199254740993}"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`) +
				ev("message_stop", `{"type":"message_stop"}`),
			want: `{"id":"msg_019","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"city":"Paris","population":9007199254740993}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":4}}`,
		},
		{
			name: "tool use without input fragments keeps starting input",
			stream: ev("message_start", `{"type":"message_start","message":{"id":"msg_020","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_02","name":"list_files","input":{}}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("message_stop", `{"type":"message_stop"}`),
			want: `{"id":"msg_020","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"tool_use","id":"toolu_02","name":"list_files","input":{}}],"usage":{"input_tokens":5,"output_tokens":1}}`,
		},
		{
			// A tool with no parameters produces no input tokens, so its
			// whole fragment stream is the one empty input_json_delta every
			// tool input opens with. That is the absence of fragments, not
			// malformed JSON: parsing it failed the entire assembly and
			// degraded the exchange to raw stream text, where the entropy
			// layer then masked the signatures and ids the record must keep
			// verbatim. 2026-08-26.
			name: "tool use whose only input fragment is empty keeps starting input",
			stream: ev("message_start", `{"type":"message_start","message":{"id":"msg_024","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_05","name":"get_time","input":{}}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`) +
				ev("message_stop", `{"type":"message_stop"}`),
			want: `{"id":"msg_024","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"tool_use","id":"toolu_05","name":"get_time","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":4}}`,
		},
		{
			name: "citations appended in delta order",
			stream: ev("message_start", `{"type":"message_start","message":{"id":"msg_021","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":2,"output_tokens":1}}}`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","citations":[]}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://a.example"}}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cited"}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://b.example"}}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("message_stop", `{"type":"message_stop"}`),
			want: `{"id":"msg_021","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"text","text":"cited","citations":[{"type":"web_search_result_location","url":"https://a.example"},{"type":"web_search_result_location","url":"https://b.example"}]}],"usage":{"input_tokens":2,"output_tokens":1}}`,
		},
		{
			name: "crlf lines and comments tolerated",
			stream: strings.ReplaceAll(
				": stream comment\n\n"+
					ev("message_start", `{"type":"message_start","message":{"id":"msg_022","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)+
					ev("message_stop", `{"type":"message_stop"}`),
				"\n", "\r\n"),
			want: `{"id":"msg_022","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := capture.Assemble([]byte(tt.stream))
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !reflect.DeepEqual(decode(t, got), decode(t, []byte(tt.want))) {
				t.Errorf("assembled object mismatch\ngot:  %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestAssembleKeepsLargeNumbersAsSourceLiterals(t *testing.T) {
	stream := ev("message_start", `{"type":"message_start","message":{"id":"msg_023","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":9007199254740993,"output_tokens":1}}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
	got, err := capture.Assemble([]byte(stream))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !bytes.Contains(got, []byte("9007199254740993")) {
		t.Errorf("number literal rewritten: %s", got)
	}
}

func TestAssembleRefusesToGuess(t *testing.T) {
	start := ev("message_start", `{"type":"message_start","message":{"id":"msg_030","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	tests := []struct {
		name   string
		stream string
	}{
		{"empty stream", ""},
		{"unknown event type", start + ev("surprise", `{"type":"surprise"}`) + ev("message_stop", `{"type":"message_stop"}`)},
		{"error event", start + ev("error", `{"type":"error","error":{"type":"overloaded_error"}}`)},
		{"malformed event data", start + ev("message_delta", `{"type":`) + ev("message_stop", `{"type":"message_stop"}`)},
		{"events before message_start", ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)},
		{"duplicate message_start", start + start + ev("message_stop", `{"type":"message_stop"}`)},
		{"missing message_stop", start},
		{"stream cut mid event", start + "event: message_stop\ndata: {\"type\":\"message_stop\"}"},
		{"event after message_stop", start + ev("message_stop", `{"type":"message_stop"}`) + ev("ping", `{"type":"ping"}`)},
		{"unknown delta type",
			start + ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"mystery_delta","text":"x"}}`) +
				ev("message_stop", `{"type":"message_stop"}`)},
		{"delta for unstarted block",
			start + ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`) +
				ev("message_stop", `{"type":"message_stop"}`)},
		{"out of order block index",
			start + ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
				ev("message_stop", `{"type":"message_stop"}`)},
		{"tool input fragments are not valid JSON",
			start + ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_03","name":"f","input":{}}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("message_stop", `{"type":"message_stop"}`)},
		{"tool input fragments left unclosed",
			start + ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_04","name":"f","input":{}}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`) +
				ev("message_stop", `{"type":"message_stop"}`)},
		{"unrecognized stream line", "garbage without a field name\n\n" + start + ev("message_stop", `{"type":"message_stop"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := capture.Assemble([]byte(tt.stream)); err == nil {
				t.Error("Assemble succeeded, want degradation error")
			}
		})
	}
}

func TestShouldRecordMatchesExactEndpointOnly(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{"POST", "/v1/messages", true},
		{"POST", "/v1/messages/count_tokens", false},
		{"POST", "/v1/messages/batches", false},
		{"GET", "/v1/messages", false},
		{"POST", "/v1/complete", false},
		{"POST", "/v1/models", false},
	}
	for _, tt := range tests {
		if got := capture.Anthropic.ShouldRecord(tt.method, tt.path); got != tt.want {
			t.Errorf("ShouldRecord(%s %s) = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}
