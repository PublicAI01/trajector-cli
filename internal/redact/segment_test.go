package redact_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/redact"
	"github.com/PublicAI01/trajector-cli/internal/sessionline"
)

func parsedLine(t *testing.T, line string) sessionline.Line {
	t.Helper()
	parsed, ok := sessionline.Parse([]byte(line))
	if !ok {
		t.Fatalf("not a session line: %s", line)
	}
	return parsed
}

const (
	fixtureSessionID = "0f1e2d3c-4b5a-4968-8776-655443322110"
	fixtureProject   = "/srv/work/project-alpha"
	pathToken        = `"[REDACTED_PATH]"`
)

func fixtureCapture() envelope.TranscriptCapture {
	return envelope.TranscriptCapture{
		ClientVersion:  "0.1.0",
		Timestamp:      "2026-09-01T10:00:05.000Z",
		ProjectIDHash:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ProjectSubpath: "apps/api",
		Injection:      envelope.InjectionProxy,
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data := strings.TrimSuffix(string(readFixture(t, name)), "\n")
	return strings.Split(data, "\n")
}

func redactSegmentLines(t *testing.T, lines string) string {
	t.Helper()
	seg := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(), lines)
	got, err := redact.RedactSegment(seg)
	if err != nil {
		t.Fatalf("RedactSegment: %v", err)
	}
	return got.Lines
}

func signaturesIn(t *testing.T, line string) []string {
	t.Helper()
	var record struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(record.Message.Content), "[") {
		return nil
	}
	var blocks []struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(record.Message.Content, &blocks); err != nil {
		t.Fatal(err)
	}
	var sigs []string
	for _, block := range blocks {
		if block.Signature != "" {
			sigs = append(sigs, block.Signature)
		}
	}
	return sigs
}

func TestRedactSegment_SignatureBytesSurvive(t *testing.T) {
	t.Parallel()
	input := string(readFixture(t, "segment_lines.jsonl"))
	var want []string
	for _, line := range fixtureLines(t, "segment_lines.jsonl") {
		want = append(want, signaturesIn(t, line)...)
	}
	if len(want) == 0 {
		t.Fatal("fixture carries no signature")
	}
	for _, sig := range want {
		if !strings.ContainsAny(sig, "+/") || !strings.HasSuffix(sig, "=") {
			t.Fatalf("fixture signature does not exercise + / =: %q", sig)
		}
	}

	got := redactSegmentLines(t, input)

	for _, sig := range want {
		if !strings.Contains(got, `"signature":"`+sig+`"`) {
			t.Errorf("signature did not survive byte for byte:\n%s", got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output lost its final newline")
	}
	if strings.Count(got, "\n") != strings.Count(input, "\n") {
		t.Errorf("line count changed: %d -> %d", strings.Count(input, "\n"), strings.Count(got, "\n"))
	}
}

func TestRedactSegment_IncompleteTrailingLineIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines string
	}{
		{name: "empty", lines: ""},
		{name: "single line without newline", lines: `{"type":"summary","summary":"x"}`},
		{name: "cut line after a complete one", lines: `{"type":"summary","summary":"x"}` + "\n" + `{"type":"user","cwd":"/srv/wo`},
		{name: "cut line ending in carriage return", lines: `{"type":"summary","summary":"x"}` + "\r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seg := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(), tc.lines)
			_, err := redact.RedactSegment(seg)
			if !errors.Is(err, redact.ErrIncompleteLine) {
				t.Fatalf("err = %v, want ErrIncompleteLine", err)
			}
		})
	}
}

func TestRedactSegment_StripsOnlyAnchoredPaths(t *testing.T) {
	t.Parallel()
	scratch := "/tmp/agent-1001/-srv-work-project-alpha/" + fixtureSessionID + "/scratchpad"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "root cwd is replaced and the key kept",
			in:   `{"type":"user","cwd":"` + fixtureProject + `","uuid":"u1"}`,
			want: `{"type":"user","cwd":` + pathToken + `,"uuid":"u1"}`,
		},
		{
			name: "cwd inside toolUseResult is an observation and stays",
			in:   `{"type":"user","cwd":"` + fixtureProject + `","toolUseResult":{"cwd":"` + fixtureProject + `","filePath":"/etc/hosts"}}`,
			want: `{"type":"user","cwd":` + pathToken + `,"toolUseResult":{"cwd":"` + fixtureProject + `","filePath":"/etc/hosts"}}`,
		},
		{
			name: "cwd spelled inside message text stays",
			in:   `{"type":"user","cwd":"` + fixtureProject + `","message":{"role":"user","content":"cwd: ` + fixtureProject + `"}}`,
			want: `{"type":"user","cwd":` + pathToken + `,"message":{"role":"user","content":"cwd: ` + fixtureProject + `"}}`,
		},
		{
			name: "cwd inside an escaped JSON string stays",
			in:   `{"cwd":"` + fixtureProject + `","toolUseResult":{"stdout":"{\"cwd\":\"` + fixtureProject + `\"}"}}`,
			want: `{"cwd":` + pathToken + `,"toolUseResult":{"stdout":"{\"cwd\":\"` + fixtureProject + `\"}"}}`,
		},
		{
			name: "cwd inside an array element stays",
			in:   `{"items":[{"cwd":"` + fixtureProject + `"}],"cwd":"` + fixtureProject + `"}`,
			want: `{"items":[{"cwd":"` + fixtureProject + `"}],"cwd":` + pathToken + `}`,
		},
		{
			name: "file_path in a tool input stays",
			in:   `{"cwd":"` + fixtureProject + `","message":{"content":[{"type":"tool_use","input":{"file_path":"` + fixtureProject + `/main.go"}}]}}`,
			want: `{"cwd":` + pathToken + `,"message":{"content":[{"type":"tool_use","input":{"file_path":"` + fixtureProject + `/main.go"}}]}}`,
		},
		{
			name: "scratchpadDirectory is replaced on an environment attachment",
			in:   `{"type":"attachment","attachment":{"type":"environment","snapshot":{"workingDirectory":"` + fixtureProject + `","scratchpadDirectory":"` + scratch + `"}}}`,
			want: `{"type":"attachment","attachment":{"type":"environment","snapshot":{"workingDirectory":"` + fixtureProject + `","scratchpadDirectory":` + pathToken + `}}}`,
		},
		{
			name: "scratchpadDirectory stays on any other attachment",
			in:   `{"type":"attachment","attachment":{"type":"queued_command","snapshot":{"scratchpadDirectory":"` + scratch + `"}}}`,
			want: `{"type":"attachment","attachment":{"type":"queued_command","snapshot":{"scratchpadDirectory":"` + scratch + `"}}}`,
		},
		{
			name: "scratchpadDirectory stays when the attachment has no type",
			in:   `{"type":"attachment","attachment":{"snapshot":{"scratchpadDirectory":"` + scratch + `"}}}`,
			want: `{"type":"attachment","attachment":{"snapshot":{"scratchpadDirectory":"` + scratch + `"}}}`,
		},
		{
			name: "environment type at the root does not stand in for the attachment type",
			in:   `{"type":"environment","attachment":{"snapshot":{"scratchpadDirectory":"` + scratch + `"}}}`,
			want: `{"type":"environment","attachment":{"snapshot":{"scratchpadDirectory":"` + scratch + `"}}}`,
		},
		{
			name: "workingDirectory stays",
			in:   `{"attachment":{"type":"environment","snapshot":{"workingDirectory":"` + fixtureProject + `"}}}`,
			want: `{"attachment":{"type":"environment","snapshot":{"workingDirectory":"` + fixtureProject + `"}}}`,
		},
		{
			name: "a key spelled with an escape addresses the same field",
			in:   `{"c\u0077d":"` + fixtureProject + `"}`,
			want: `{"c\u0077d":` + pathToken + `}`,
		},
		{
			name: "a cwd that is not a string stays",
			in:   `{"cwd":null,"type":"user"}`,
			want: `{"cwd":null,"type":"user"}`,
		},
		{
			name: "a line without anchored fields is untouched",
			in:   `{"type":"summary","summary":"Fix the widget","leafUuid":"u9"}`,
			want: `{"type":"summary","summary":"Fix the widget","leafUuid":"u9"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := redactSegmentLines(t, tc.in+"\n"); got != tc.want+"\n" {
				t.Errorf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestRedactSegment_OtherBytesUnchanged(t *testing.T) {
	t.Parallel()
	prefix := `{"z":1e5,"n":-0.0,"html":"<a href=\"x\">&amp;</a>","u":"caf\u00e9 \ud83d\ude00 é",  "cwd" : `
	suffix := `,"nested":{"k":[1,2,{"cwd":"/srv/x"}],"b":true},"a":"last"}`
	in := prefix + `"` + fixtureProject + `"` + suffix + "\n"

	got := redactSegmentLines(t, in)

	want := prefix + pathToken + suffix + "\n"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix+"\n") {
		t.Errorf("bytes around the anchored value changed")
	}
	if !json.Valid([]byte(strings.TrimSuffix(got, "\n"))) {
		t.Errorf("output is not valid JSON: %s", got)
	}
}

func TestRedactSegment_KeepsALineThatIsNotASessionLine(t *testing.T) {
	t.Parallel()
	lines := `{"type":"user","cwd":"` + fixtureProject + `"}` + "\n" +
		`plain text, no object here` + "\n" +
		`["x"]` + "\n"

	got := strings.Split(strings.TrimSuffix(redactSegmentLines(t, lines), "\n"), "\n")

	if len(got) != 3 {
		t.Fatalf("lines = %d, want 3: %v", len(got), got)
	}
	if !strings.Contains(got[0], `"cwd":`+pathToken) {
		t.Errorf("anchored path not stripped: %s", got[0])
	}
	if got[1] != `plain text, no object here` {
		t.Errorf("got %q, want the bytes as they were", got[1])
	}
	if got[2] != `["x"]` {
		t.Errorf("got %q, want the bytes as they were", got[2])
	}
}

func TestRedactSegment_EnvelopeFieldsAreNotScanned(t *testing.T) {
	t.Parallel()
	secretLike := highEntropySecret
	capture := fixtureCapture()
	capture.ProjectSubpath = "apps/" + secretLike
	capture.ProjectIDHash = secretLike
	seg := envelope.NewSegment(secretLike, "subagents/agent-"+secretLike+".jsonl", 3, capture,
		`{"type":"user","message":{"role":"user","content":"key `+secretLike+` here"}}`+"\n")

	got, err := redact.RedactSegment(seg)
	if err != nil {
		t.Fatal(err)
	}

	if got.SessionID != seg.SessionID || got.File != seg.File || got.SegmentIndex != seg.SegmentIndex || got.Capture != seg.Capture ||
		got.RecordID != seg.RecordID || got.SchemaVersion != seg.SchemaVersion || got.Source != seg.Source || got.RecordKind != seg.RecordKind {
		t.Errorf("envelope changed:\n got %+v\nwant %+v", got, seg)
	}
	if strings.Contains(got.Lines, secretLike) {
		t.Errorf("the same value inside the lines was not masked: %s", got.Lines)
	}
}

func TestRedactMetaSnapshot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		content     string
		wantContent string
	}{
		{
			name:        "email in the description is masked and the layout kept",
			content:     string(readFixture(t, "meta_snapshot.json")),
			wantContent: strings.Replace(string(readFixture(t, "meta_snapshot.json")), "someone@example.org", "[REDACTED_EMAIL]", 1),
		},
		{
			name:        "content without anything to mask is returned byte for byte",
			content:     `{"agentId":"a1b2c3d4","toolUseId":"toolu_01FixtureToolUse0002","spawnDepth":1,"description":"List <b>the</b> widgets"}`,
			wantContent: `{"agentId":"a1b2c3d4","toolUseId":"toolu_01FixtureToolUse0002","spawnDepth":1,"description":"List <b>the</b> widgets"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const file = "subagents/agent-a1b2c3d4.meta.json"
			snap, err := envelope.NewMetaSnapshot(fixtureSessionID, file, fixtureCapture(), []byte(tc.content))
			if err != nil {
				t.Fatal(err)
			}

			got, err := redact.RedactMetaSnapshot(snap)
			if err != nil {
				t.Fatal(err)
			}

			if string(got.Content) != tc.wantContent {
				t.Errorf("\n got %s\nwant %s", got.Content, tc.wantContent)
			}
			wantID, err := envelope.MetaSnapshotRecordID(fixtureSessionID, file, got.Content)
			if err != nil {
				t.Fatal(err)
			}
			if got.RecordID != wantID {
				t.Errorf("record_id %q does not name the masked content (%q)", got.RecordID, wantID)
			}
			if changed := tc.wantContent != tc.content; changed == (got.RecordID == snap.RecordID) {
				t.Errorf("content changed = %v but record_id changed = %v", changed, got.RecordID != snap.RecordID)
			}
			if got.SessionID != snap.SessionID || got.File != snap.File || got.Capture != snap.Capture ||
				got.SchemaVersion != snap.SchemaVersion || got.Source != snap.Source || got.RecordKind != snap.RecordKind {
				t.Errorf("envelope changed:\n got %+v\nwant %+v", got, snap)
			}
		})
	}
}

func TestRedactMetaSnapshot_RefusesContentThatIsNotJSON(t *testing.T) {
	t.Parallel()
	snap := envelope.MetaSnapshot{SessionID: fixtureSessionID, File: "subagents/agent-a1.meta.json", Content: json.RawMessage(`not json`)}
	if _, err := redact.RedactMetaSnapshot(snap); err == nil {
		t.Fatal("expected an error for content that is not a JSON document")
	}
}

func TestAbsolutePathFields_KnowsTheAnchoredList(t *testing.T) {
	t.Parallel()
	t.Run("fixture lines report nothing", func(t *testing.T) {
		t.Parallel()
		for i, line := range fixtureLines(t, "segment_lines.jsonl") {
			got := redact.AbsolutePathFields(parsedLine(t, line))
			if len(got) != 0 {
				t.Errorf("line %d: unexpected absolute path fields %v", i, got)
			}
		}
	})
	t.Run("a new root field is reported", func(t *testing.T) {
		t.Parallel()
		got := redact.AbsolutePathFields(parsedLine(t, string(readFixture(t, "drift_line.jsonl"))))
		if len(got) != 1 || got[0] != "$.someNewPath" {
			t.Errorf("got %v, want [$.someNewPath]", got)
		}
	})

	scratch := "/tmp/agent-1001/-srv-work-project-alpha/" + fixtureSessionID + "/scratchpad"
	cases := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "windows drive root is reported",
			line: `{"newDir":"C:\\work\\thing","other":"D:/work"}`,
			want: []string{"$.newDir", "$.other"},
		},
		{
			name: "a value with whitespace is prose, not a path",
			line: `{"note":"/srv/work and more","cmd":"/bin/sh -c ls"}`,
			want: nil,
		},
		{
			name: "a relative path is not reported",
			line: `{"file":"subagents/agent-a1.jsonl","drive":"C:relative"}`,
			want: nil,
		},
		{
			name: "deeper layers are not looked at",
			line: `{"toolUseResult":{"filePath":"/etc/hosts","file":{"filePath":"/srv/x"}},"message":{"content":[{"input":{"file_path":"/srv/y"}}]},"attachment":{"filename":"/srv/z"}}`,
			want: nil,
		},
		{
			name: "a new field under attachment.snapshot is reported",
			line: `{"attachment":{"type":"environment","snapshot":{"workingDirectory":"/srv/w","newDir":"/srv/n"}}}`,
			want: []string{"$.attachment.snapshot.newDir"},
		},
		{
			name: "scratchpadDirectory outside an environment attachment is reported",
			line: `{"attachment":{"type":"queued_command","snapshot":{"scratchpadDirectory":"` + scratch + `"}}}`,
			want: []string{"$.attachment.snapshot.scratchpadDirectory"},
		},
		{
			name: "values under an array are not looked at",
			line: `{"paths":["/srv/a","/srv/b"],"rows":[{"dir":"/srv/c"}]}`,
			want: nil,
		},
		{
			name: "a slash command kept as the last prompt is text, not a location",
			line: `{"type":"last-prompt","lastPrompt":"/clear","sessionId":"` + fixtureSessionID + `"}`,
			want: nil,
		},
		{
			name: "lastPrompt on any other line is reported",
			line: `{"type":"user","lastPrompt":"/clear"}`,
			want: []string{"$.lastPrompt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redact.AbsolutePathFields(parsedLine(t, tc.line))
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAbsolutePathFields_NamesNoKeyThatCouldBeAPath(t *testing.T) {
	t.Parallel()
	const keyToken = "[REDACTED_KEY]"
	cases := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "an absolute path in key position",
			line: `{"/home/jdoe/work/project-alpha/main.go":"/home/jdoe/work/project-alpha/main.go"}`,
			want: []string{"$." + keyToken},
		},
		{
			name: "a windows path in key position",
			line: `{"C:\\Users\\jdoe\\work":"C:\\Users\\jdoe\\work\\notes.md"}`,
			want: []string{"$." + keyToken},
		},
		{
			name: "a key holding a user name and spaces",
			line: `{"jdoe project files":"/srv/work/project-alpha"}`,
			want: []string{"$." + keyToken},
		},
		{
			name: "a key spelled with escapes still hides its separators",
			line: `{"\u002fhome\u002fjdoe\u002fwork":"/home/jdoe/work"}`,
			want: []string{"$." + keyToken},
		},
		{
			name: "a key that looks like a file name",
			line: `{"agent-a1.jsonl":"/srv/work/project-alpha"}`,
			want: []string{"$." + keyToken},
		},
		{
			name: "two path keys on one line report one name",
			line: `{"/home/jdoe/a.go":"/home/jdoe/a.go","/home/jdoe/b.go":"/home/jdoe/b.go"}`,
			want: []string{"$." + keyToken},
		},
		{
			name: "a path key under the snapshot layer",
			line: `{"attachment":{"type":"environment","snapshot":{"/home/jdoe/work":"/home/jdoe/work/main.go"}}}`,
			want: []string{"$.attachment.snapshot." + keyToken},
		},
		{
			name: "a plain field name is still named in full",
			line: `{"someNewPath":"/srv/elsewhere/thing","another_new-Path2":"/srv/elsewhere/other"}`,
			want: []string{"$.someNewPath", "$.another_new-Path2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redact.AbsolutePathFields(parsedLine(t, tc.line))
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAbsolutePathFields_NamesNoPathFromLinesTheReaderNeverKeeps(t *testing.T) {
	t.Parallel()
	lines := fixtureLines(t, "file_history_lines.jsonl")
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"/home/jdoe/work/project-alpha", `C:\\Users\\jdoe`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fixture no longer holds %q in key position", want)
		}
	}
	for i, line := range lines {
		got := redact.AbsolutePathFields(parsedLine(t, line))
		for _, name := range got {
			if strings.ContainsAny(name, `/\: `) {
				t.Errorf("line %d: name %q carries a path", i, name)
			}
			if strings.Contains(name, "jdoe") || strings.Contains(name, "project-alpha") {
				t.Errorf("line %d: name %q carries a value from the line", i, name)
			}
		}
	}
}

// TestPIIIsMaskedWithoutConfiguration runs its assertion in a fresh copy
// of the test binary, so no configurePII call from any other test in
// this process can have set the state it observes.
func TestPIIIsMaskedWithoutConfiguration(t *testing.T) {
	const marker = "REDACT_TEST_FRESH_PROCESS"
	if os.Getenv(marker) == "1" {
		cases := []struct{ name, in, want string }{
			{name: "email", in: "contact someone@example.org now", want: "contact [REDACTED_EMAIL] now"},
			{name: "phone", in: "call 555-123-4567", want: "call [REDACTED_PHONE]"},
			{name: "email in a segment", in: `{"type":"user","message":{"content":"someone@example.org"}}`, want: `{"type":"user","message":{"content":"[REDACTED_EMAIL]"}}`},
		}
		for _, tc := range cases {
			var got string
			if strings.HasPrefix(tc.in, "{") {
				got = strings.TrimSuffix(redactSegmentLines(t, tc.in+"\n"), "\n")
			} else {
				got = redactedField(t, tc.in)
			}
			if got != tc.want {
				t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
			}
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestPIIIsMaskedWithoutConfiguration$", "-test.v")
	cmd.Env = append(os.Environ(), marker+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh process failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestPIIIsMaskedWithoutConfiguration") {
		t.Fatalf("fresh process did not run the assertion:\n%s", out)
	}
}

func TestJSONFieldPolicy_GitBranchIsDeterministic(t *testing.T) {
	t.Parallel()
	const randomLooking = "feat/aB3dEfGh1JkLmN0pQrStUvWxYz2Q9"
	cases := []struct{ name, in, want string }{
		{
			name: "a random-looking branch name under gitBranch survives",
			in:   `{"gitBranch":"` + randomLooking + `","type":"user"}`,
			want: `{"gitBranch":"` + randomLooking + `","type":"user"}`,
		},
		{
			name: "the same value under an ordinary key is masked",
			in:   `{"note":"` + randomLooking + `"}`,
			want: `{"note":"feat/REDACTED"}`,
		},
		{
			name: "a credential under gitBranch is still masked",
			in:   `{"gitBranch":"sbp_a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"}`,
			want: `{"gitBranch":"REDACTED"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := redactedString(t, tc.in); got != tc.want {
				t.Errorf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}
