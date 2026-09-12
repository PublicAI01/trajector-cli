package sessionline_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/sessionline"
)

func TestParseAcceptsOnlyAJSONObject(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want bool
	}{
		{name: "an object", line: `{"type":"user"}`, want: true},
		{name: "an empty object", line: `{}`, want: true},
		{name: "an object behind leading space", line: "  \t" + `{"type":"user"}`, want: true},
		{name: "an object with a trailing newline", line: `{"type":"user"}` + "\n", want: true},
		{name: "an empty line", line: ``, want: false},
		{name: "a line of spaces", line: "   ", want: false},
		{name: "text that is not JSON", line: `hello`, want: false},
		{name: "a cut object", line: `{"cwd":"/srv/wo`, want: false},
		{name: "an array", line: `["a"]`, want: false},
		{name: "a bare string", line: `"a"`, want: false},
		{name: "a null", line: `null`, want: false},
		{name: "a number", line: `12`, want: false},
		{name: "an object followed by another value", line: `{"a":1} {"b":2}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := sessionline.Parse([]byte(tc.line)); ok != tc.want {
				t.Errorf("ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestTextIsTheLineAsItWas(t *testing.T) {
	t.Parallel()
	const line = `{"type":"user","message":{"content":"a\u002fb"},"n":1.50}`
	parsed, ok := sessionline.Parse([]byte(line))
	if !ok {
		t.Fatal("not a session line")
	}
	if parsed.Text() != line {
		t.Errorf("got %q, want %q", parsed.Text(), line)
	}
}

func TestFieldsReadsWhatTheFormatStates(t *testing.T) {
	t.Parallel()
	const line = `{"type":"assistant","subtype":"api_error","entrypoint":"cli",` +
		`"relocatedCwd":"/srv/work/project-beta","isSidechain":true,"apiBlockIndex":3,` +
		`"agentId":"a1","toolUseId":"toolu_1","parentAgentId":"a0","parentSessionId":"sid-0",` +
		`"attachment":{"type":"environment"},"message":{"id":"msg_1","role":"assistant"}}`
	parsed, ok := sessionline.Parse([]byte(line))
	if !ok {
		t.Fatal("not a session line")
	}

	got := parsed.Fields()

	want := sessionline.Fields{
		Type:            "assistant",
		Subtype:         "api_error",
		LaunchSurface:   "cli",
		RelocatedCwd:    "/srv/work/project-beta",
		MessageID:       "msg_1",
		AttachmentType:  "environment",
		AgentID:         "a1",
		ToolUseID:       "toolu_1",
		ParentAgentID:   "a0",
		ParentSessionID: "sid-0",
		Sidechain:       true,
		BlockIndex:      3,
		HasBlockIndex:   true,
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFieldsReadsAFieldOfAnotherShapeAsAbsent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want sessionline.Fields
	}{
		{
			name: "a type that is a number leaves the rest readable",
			line: `{"type":7,"message":{"id":"msg_1"}}`,
			want: sessionline.Fields{MessageID: "msg_1"},
		},
		{
			name: "a message that is text",
			line: `{"type":"assistant","message":"hello"}`,
			want: sessionline.Fields{Type: "assistant"},
		},
		{
			name: "a message id that is an object",
			line: `{"type":"assistant","message":{"id":{"a":1}}}`,
			want: sessionline.Fields{Type: "assistant"},
		},
		{
			name: "a sidechain flag that is text",
			line: `{"type":"user","isSidechain":"yes"}`,
			want: sessionline.Fields{Type: "user"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parsed, ok := sessionline.Parse([]byte(tc.line))
			if !ok {
				t.Fatal("not a session line")
			}
			if got := parsed.Fields(); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFieldsReportsNoBlockIndexWhereTheLineCarriesNoWholeNumber(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		`{"type":"assistant"}`,
		`{"type":"assistant","apiBlockIndex":null}`,
		`{"type":"assistant","apiBlockIndex":1.5}`,
		`{"type":"assistant","apiBlockIndex":"2"}`,
	} {
		parsed, ok := sessionline.Parse([]byte(line))
		if !ok {
			t.Fatalf("not a session line: %s", line)
		}
		if got := parsed.Fields(); got.HasBlockIndex {
			t.Errorf("%s: HasBlockIndex = true, want false", line)
		}
	}
}

func TestMessageHasReportsAKeyWhateverStandsUnderIt(t *testing.T) {
	t.Parallel()
	parsed, ok := sessionline.Parse([]byte(`{"type":"assistant","message":{"id":"msg_1","model":"a-model","usage":{"input_tokens":1},"stop_reason":null}}`))
	if !ok {
		t.Fatal("not a session line")
	}
	for _, key := range []string{"id", "model", "usage", "stop_reason"} {
		if !parsed.MessageHas(key) {
			t.Errorf("MessageHas(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"role", "content", ""} {
		if parsed.MessageHas(key) {
			t.Errorf("MessageHas(%q) = true, want false", key)
		}
	}
}

func TestMessageHasReportsNothingWhereThereIsNoMessageObject(t *testing.T) {
	t.Parallel()
	for _, line := range []string{`{"type":"user"}`, `{"type":"user","message":"hello"}`, `{"type":"user","message":[1]}`} {
		parsed, ok := sessionline.Parse([]byte(line))
		if !ok {
			t.Fatalf("not a session line: %s", line)
		}
		if parsed.MessageHas("id") {
			t.Errorf("%s: MessageHas = true, want false", line)
		}
	}
}

func TestParseKeepsALineOfAnySizeAsOneValue(t *testing.T) {
	t.Parallel()
	line := `{"type":"user","message":{"content":"` + strings.Repeat("a", 4096) + `"}}`
	parsed, ok := sessionline.Parse([]byte(line))
	if !ok {
		t.Fatal("not a session line")
	}
	if len(parsed.Text()) != len(line) || parsed.Fields().Type != "user" {
		t.Errorf("got %d bytes and type %q", len(parsed.Text()), parsed.Fields().Type)
	}
}

func TestFieldsReportsAReasoningFieldThatIsThereAndEmpty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want bool
	}{
		{
			name: "a reasoning field with nothing in it",
			line: `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`,
			want: true,
		},
		{
			name: "a reasoning field holding text",
			line: `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":"weighing two options","signature":"sig"}]}}`,
		},
		{
			name: "no reasoning field at all",
			line: `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"done"}]}}`,
		},
		{
			name: "a reasoning field of another shape",
			line: `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":null}]}}`,
		},
		{
			name: "an empty one among other blocks",
			line: `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"done"},{"type":"thinking","thinking":""}]}}`,
			want: true,
		},
		{
			name: "a message whose content is text",
			line: `{"type":"assistant","message":{"id":"msg_1","content":"done"}}`,
		},
		{
			name: "no message at all",
			line: `{"type":"assistant"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parsed, ok := sessionline.Parse([]byte(tc.line))
			if !ok {
				t.Fatal("not a session line")
			}
			if got := parsed.Fields().EmptyReasoning; got != tc.want {
				t.Errorf("EmptyReasoning = %v, want %v", got, tc.want)
			}
		})
	}
}
