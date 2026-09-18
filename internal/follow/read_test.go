package follow_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
)

const (
	root      = "/p"
	sessionID = "sid-main"
)

var capture = envelope.Capture{
	ClientVersion: "0.1.0",
	Timestamp:     "2026-01-01T00:00:00Z",
	ProjectIDHash: project,
	Injection:     envelope.InjectionProxy,
}

func fixtureLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.SplitAfter(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func userLine(n int) string {
	return fmt.Sprintf(`{"type":"user","cwd":%q,"sessionId":%q,"message":{"role":"user","content":"u%d"},"uuid":"u-%d"}`+"\n", root, sessionID, n, n)
}

func assistantLine(id string, block int, text string) string {
	return fmt.Sprintf(`{"type":"assistant","cwd":%q,"sessionId":%q,"apiBlockIndex":%d,"message":{"id":%q,"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", root, sessionID, block, id, text)
}

func relocatedLine(cwd string) string {
	return fmt.Sprintf(`{"type":"relocated","sessionId":%q,"relocatedCwd":%q}`+"\n", sessionID, cwd)
}

func mainPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), sessionID+".jsonl")
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, content string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// replaceFile gives path a new inode with content, as a rewrite by
// rename does.
func replaceFile(t *testing.T, path string, content string) {
	t.Helper()
	tmp := path + ".new"
	writeFile(t, tmp, content)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, f follow.File) follow.ReadResult {
	t.Helper()
	res, err := follow.Read(f, capture, follow.ReadOptions{Root: root})
	if err != nil {
		t.Fatalf("Read(%+v): %v", f, err)
	}
	return res
}

// segmentLines returns the lines of the one segment a read produced,
// or nil when it produced none.
func segmentLines(t *testing.T, res follow.ReadResult) []string {
	t.Helper()
	switch len(res.Segments) {
	case 0:
		return nil
	case 1:
		lines := strings.SplitAfter(res.Segments[0].Lines, "\n")
		return lines[:len(lines)-1]
	default:
		t.Fatalf("Read produced %d segments, want at most one", len(res.Segments))
		return nil
	}
}

func wantLines(t *testing.T, res follow.ReadResult, want ...string) {
	t.Helper()
	got := segmentLines(t, res)
	if len(want) == 0 {
		if got != nil {
			t.Errorf("segment lines = %q, want no segment", got)
		}
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("segment lines = %q\nwant %q", got, want)
	}
	if len(res.Segments) == 1 && !strings.HasSuffix(res.Segments[0].Lines, "\n") {
		t.Errorf("segment does not end in a newline")
	}
}

func wantCursor(t *testing.T, got follow.File, offset int64, next int, ids ...string) {
	t.Helper()
	if got.Offset != offset || got.NextSegment != next {
		t.Errorf("cursor offset/next = %d/%d, want %d/%d", got.Offset, got.NextSegment, offset, next)
	}
	if !slices.Equal(got.MessageIDs, ids) {
		t.Errorf("cursor message ids = %q, want %q", got.MessageIDs, ids)
	}
	if got.Size < got.Offset {
		t.Errorf("cursor size %d is below offset %d", got.Size, got.Offset)
	}
}

func TestRead_TrailingHalfLineWaitsForNextRead(t *testing.T) {
	path := mainPath(t)
	half := userLine(3)
	writeFile(t, path, userLine(1)+userLine(2)+half[:len(half)/2])

	first := read(t, follow.File{Path: path})
	wantLines(t, first, userLine(1), userLine(2))
	wantCursor(t, first.File, int64(len(userLine(1)+userLine(2))), 1)

	appendFile(t, path, half[len(half)/2:])
	second := read(t, first.File)
	wantLines(t, second, userLine(3))
	wantCursor(t, second.File, int64(len(userLine(1)+userLine(2)+userLine(3))), 2)
	if second.Reaction != follow.Continue {
		t.Errorf("Reaction = %v, want Continue", second.Reaction)
	}
}

func TestRead_ConsumesOnlyCompleteLines(t *testing.T) {
	twoObjectsOnOneLine := strings.ReplaceAll(userLine(1), "\n", "\r") + userLine(2)
	tests := []struct {
		name         string
		content      string
		want         []string
		wantConsumed int
	}{
		{name: "empty file", content: ""},
		{name: "one line without newline", content: strings.TrimSuffix(userLine(1), "\n")},
		{name: "one complete line", content: userLine(1), want: []string{userLine(1)}, wantConsumed: len(userLine(1))},
		{name: "complete line then an open brace", content: userLine(1) + "{", want: []string{userLine(1)}, wantConsumed: len(userLine(1))},
		{name: "carriage return is no line boundary", content: twoObjectsOnOneLine, wantConsumed: len(twoObjectsOnOneLine)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := mainPath(t)
			writeFile(t, path, tt.content)
			res := read(t, follow.File{Path: path})
			wantLines(t, res, tt.want...)
			next := 0
			if tt.want != nil {
				next = 1
			}
			wantCursor(t, res.File, int64(tt.wantConsumed), next)
		})
	}
}

func TestRead_SegmentIndexGrowsAndSurvivesRewrite(t *testing.T) {
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	first := read(t, follow.File{Path: path})
	appendFile(t, path, userLine(2))
	second := read(t, first.File)
	replaceFile(t, path, userLine(3))
	third := read(t, second.File)
	appendFile(t, path, userLine(4))
	fourth := read(t, third.File)

	if third.Reaction != follow.Rewrite {
		t.Errorf("after rename Reaction = %v, want Rewrite", third.Reaction)
	}
	var indexes []int
	for _, res := range []follow.ReadResult{first, second, third, fourth} {
		for _, s := range res.Segments {
			indexes = append(indexes, s.SegmentIndex)
		}
	}
	if want := []int{0, 1, 2, 3}; !slices.Equal(indexes, want) {
		t.Errorf("segment indexes = %v, want %v", indexes, want)
	}
	wantLines(t, third, userLine(3))
	wantCursor(t, fourth.File, int64(len(userLine(3)+userLine(4))), 4)
}

func TestRead_RewriteRereadsAndSkipsSeenMessageIDs(t *testing.T) {
	path := mainPath(t)
	writeFile(t, path, assistantLine("msg_a", 0, "a")+assistantLine("msg_b", 0, "b"))
	first := read(t, follow.File{Path: path})
	wantCursor(t, first.File, first.File.Size, 1, "msg_a", "msg_b")

	input := first.File
	inputIDs := append([]string(nil), input.MessageIDs...)
	// Same id, changed text: the copy sent first stands.
	replaceFile(t, path, assistantLine("msg_a", 0, "changed")+assistantLine("msg_c", 0, "c"))
	second := read(t, input)

	if second.Reaction != follow.Rewrite {
		t.Errorf("Reaction = %v, want Rewrite", second.Reaction)
	}
	wantLines(t, second, assistantLine("msg_c", 0, "c"))
	wantCursor(t, second.File, second.File.Size, 2, "msg_a", "msg_b", "msg_c")
	if !slices.Equal(input.MessageIDs, inputIDs) {
		t.Errorf("input cursor ids changed to %q", input.MessageIDs)
	}
}

func TestRead_OneMessageAcrossSeveralLinesIsKeptWhole(t *testing.T) {
	block0, block1, block2 := assistantLine("msg_a", 0, "x"), assistantLine("msg_a", 1, "y"), assistantLine("msg_a", 2, "z")
	tests := []struct {
		name string
		run  func(t *testing.T, path string) follow.ReadResult
		want []string
		ids  []string
	}{
		{
			name: "all blocks within one read",
			run: func(t *testing.T, path string) follow.ReadResult {
				writeFile(t, path, block0+block1+block2)
				return read(t, follow.File{Path: path})
			},
			want: []string{block0, block1, block2},
			ids:  []string{"msg_a"},
		},
		{
			name: "later block arrives after the id was consumed",
			run: func(t *testing.T, path string) follow.ReadResult {
				writeFile(t, path, block0+block1)
				first := read(t, follow.File{Path: path})
				appendFile(t, path, block2)
				return read(t, first.File)
			},
			want: []string{block2},
			ids:  []string{"msg_a"},
		},
		{
			name: "new message met during a rewrite pass",
			run: func(t *testing.T, path string) follow.ReadResult {
				writeFile(t, path, assistantLine("msg_old", 0, "o"))
				first := read(t, follow.File{Path: path})
				replaceFile(t, path, block0+block1)
				return read(t, first.File)
			},
			want: []string{block0, block1},
			ids:  []string{"msg_old", "msg_a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := tt.run(t, mainPath(t))
			wantLines(t, res, tt.want...)
			if !slices.Equal(res.File.MessageIDs, tt.ids) {
				t.Errorf("cursor message ids = %q, want %q", res.File.MessageIDs, tt.ids)
			}
		})
	}
}

func TestRead_LinesWithoutMessageIDAreSentAgainAfterRewrite(t *testing.T) {
	path := mainPath(t)
	writeFile(t, path, userLine(1)+assistantLine("msg_a", 0, "a"))
	first := read(t, follow.File{Path: path})
	replaceFile(t, path, userLine(1)+assistantLine("msg_a", 0, "a"))
	second := read(t, first.File)

	wantLines(t, second, userLine(1))
	wantCursor(t, second.File, second.File.Size, 2, "msg_a")
}

func TestRead_FileHistoryLinesNeverEnterASegment(t *testing.T) {
	lines := fixtureLines(t)
	var fileHistory, kept []string
	for _, l := range lines {
		switch {
		case strings.Contains(l, `"type":"file-history-`):
			fileHistory = append(fileHistory, l)
		case strings.Contains(l, `"type":"bridge-session"`):
		default:
			kept = append(kept, l)
		}
	}
	if len(fileHistory) != 2 {
		t.Fatalf("fixture has %d file-history lines, want 2", len(fileHistory))
	}

	path := mainPath(t)
	writeFile(t, path, strings.Join(lines, ""))
	first := read(t, follow.File{Path: path})
	wantLines(t, first, kept...)

	appendFile(t, path, fileHistory[0])
	second := read(t, first.File)
	wantLines(t, second)
	if second.File.Offset != second.File.Size {
		t.Errorf("a read of dropped lines only left offset %d below size %d", second.File.Offset, second.File.Size)
	}

	appendFile(t, path, fileHistory[1]+userLine(9))
	third := read(t, second.File)
	wantLines(t, third, userLine(9))
	if got := []int{first.Segments[0].SegmentIndex, third.Segments[0].SegmentIndex}; !slices.Equal(got, []int{0, 1}) {
		t.Errorf("segment indexes = %v, want consecutive [0 1]", got)
	}
}

func TestRead_BridgeSessionLineIsDropped(t *testing.T) {
	bridge := `{"type":"bridge-session","sessionId":"sid-main","bridgeSessionId":"b","lastSequenceNum":1,"ownerAccountUuid":"o","ownerOrganizationUuid":"g"}` + "\n"
	path := mainPath(t)
	writeFile(t, path, userLine(1)+bridge+userLine(2))
	res := read(t, follow.File{Path: path})
	wantLines(t, res, userLine(1), userLine(2))
	wantCursor(t, res.File, int64(len(userLine(1)+bridge+userLine(2))), 1)
}

func TestRead_RelocatedOutOfConsentStops(t *testing.T) {
	path := mainPath(t)
	before := userLine(1) + assistantLine("msg_a", 0, "a")
	out := relocatedLine("/elsewhere")
	writeFile(t, path, before+out+userLine(2)+assistantLine("msg_b", 0, "b"))

	res := read(t, follow.File{Path: path})
	if !res.Stopped {
		t.Fatalf("Stopped = false, want true")
	}
	wantLines(t, res, userLine(1), assistantLine("msg_a", 0, "a"))
	wantCursor(t, res.File, int64(len(before+out)), 1, "msg_a")
}

func TestRead_RelocatedIntoConsentStartsAfterThatLine(t *testing.T) {
	outside := userLine(1) + assistantLine("msg_out", 0, "o")
	in := relocatedLine(root)
	after := userLine(2) + assistantLine("msg_in", 0, "i")
	tests := []struct {
		name     string
		start    func(t *testing.T, path string) follow.File
		want     []string
		wantNext int
	}{
		{
			name: "first read of a file that moved in before registration",
			start: func(t *testing.T, path string) follow.File {
				writeFile(t, path, outside+in+after)
				return follow.File{Path: path}
			},
			want:     []string{userLine(2), assistantLine("msg_in", 0, "i")},
			wantNext: 1,
		},
		{
			// The rewrite pass sends only what has no id: what has one was
			// consumed by the first read.
			name: "rewrite pass over the same content",
			start: func(t *testing.T, path string) follow.File {
				writeFile(t, path, outside+in+after)
				first := read(t, follow.File{Path: path})
				replaceFile(t, path, outside+in+after)
				return first.File
			},
			want:     []string{userLine(2)},
			wantNext: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := mainPath(t)
			res := read(t, tt.start(t, path))
			if res.Stopped {
				t.Errorf("Stopped = true, want false")
			}
			wantLines(t, res, tt.want...)
			wantCursor(t, res.File, int64(len(outside+in+after)), tt.wantNext, "msg_in")
		})
	}
}

func TestRead_RelocatedWithinConsentContinues(t *testing.T) {
	worktree := "/p-worktree"
	tests := []struct {
		name string
		opts follow.ReadOptions
		cwd  string
	}{
		{name: "back to the root", opts: follow.ReadOptions{Root: root}, cwd: root},
		{name: "a directory the caller covers", opts: follow.ReadOptions{Root: root, Authorized: func(p string) bool { return p == worktree }}, cwd: worktree},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := mainPath(t)
			writeFile(t, path, userLine(1))
			first := read(t, follow.File{Path: path})
			move := relocatedLine(tt.cwd)
			appendFile(t, path, move+userLine(2))

			res, err := follow.Read(first.File, capture, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Stopped {
				t.Errorf("Stopped = true, want false")
			}
			wantLines(t, res, move, userLine(2))
			wantCursor(t, res.File, int64(len(userLine(1)+move+userLine(2))), 2)
		})
	}
}

func TestRead_AuthorizedDefaultsToTheRootAlone(t *testing.T) {
	path := mainPath(t)
	writeFile(t, path, userLine(1)+relocatedLine(root+"/sub")+userLine(2))
	res := read(t, follow.File{Path: path})
	if !res.Stopped {
		t.Errorf("a move to a subdirectory did not stop reading")
	}
	wantLines(t, res, userLine(1))
}

func TestRead_SignatureBytesSurvive(t *testing.T) {
	path := mainPath(t)
	lines := fixtureLines(t)
	writeFile(t, path, strings.Join(lines, ""))
	res := read(t, follow.File{Path: path})
	if len(res.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(res.Segments))
	}
	signatures := 0
	for _, l := range lines {
		i := strings.Index(l, `"signature":"`)
		if i < 0 {
			continue
		}
		signatures++
		sig := l[i+len(`"signature":"`):]
		sig = sig[:strings.IndexByte(sig, '"')]
		if !strings.ContainsAny(sig, "+/") || !strings.HasSuffix(sig, "==") {
			t.Fatalf("fixture signature %q lacks the characters the check relies on", sig)
		}
		if !strings.Contains(res.Segments[0].Lines, l) {
			t.Errorf("line with signature %q is not in the segment byte for byte", sig)
		}
	}
	if signatures == 0 {
		t.Fatal("fixture has no signature")
	}
}

func TestRead_EmptyThinkingBlockIsKept(t *testing.T) {
	path := mainPath(t)
	lines := fixtureLines(t)
	writeFile(t, path, strings.Join(lines, ""))
	res := read(t, follow.File{Path: path})
	if len(res.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(res.Segments))
	}
	empty := 0
	for _, l := range lines {
		if strings.Contains(l, `{"type":"thinking","thinking":"",`) {
			empty++
			if !strings.Contains(res.Segments[0].Lines, l) {
				t.Errorf("line with an empty thinking block is not in the segment")
			}
		}
	}
	if empty == 0 {
		t.Fatal("fixture has no empty thinking block")
	}
}

func TestRead_VanishedFileYieldsNothing(t *testing.T) {
	path := mainPath(t)
	tests := []struct {
		name string
		file follow.File
	}{
		{name: "main file", file: follow.File{Path: path, Inode: 5, Size: 10, Offset: 10, NextSegment: 2, MessageIDs: []string{"msg_a"}}},
		{name: "agent metadata file", file: follow.File{Path: filepath.Join(filepath.Dir(path), sessionID, "subagents", "agent-1.meta.json"), Size: 3, Offset: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := read(t, tt.file)
			if res.Reaction != follow.Vanished {
				t.Errorf("Reaction = %v, want Vanished", res.Reaction)
			}
			if len(res.Segments) != 0 || len(res.Snapshots) != 0 || res.Stopped {
				t.Errorf("result = %+v, want no records", res)
			}
			if !reflect.DeepEqual(res.File, tt.file) {
				t.Errorf("cursor = %+v, want the input %+v", res.File, tt.file)
			}
		})
	}
}

func TestRead_MetaSnapshotLastWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sessionID, "subagents", "agent-7.meta.json")
	fixture, err := os.ReadFile(filepath.Join("testdata", "agent.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := string(fixture)
	second := strings.Replace(first, `"description":"d"`, `"description":"e"`, 1)
	if len(first) != len(second) {
		t.Fatal("the two contents must have the same size")
	}

	writeFile(t, path, first)
	r1 := read(t, follow.File{Path: path})
	r2 := read(t, r1.File)
	writeFile(t, path, second)
	r3 := read(t, r2.File)
	replaceFile(t, path, second)
	r4 := read(t, r3.File)
	writeFile(t, path, first)
	r5 := read(t, r4.File)

	var got []string
	for _, r := range []follow.ReadResult{r1, r2, r3, r4, r5} {
		if len(r.Segments) != 0 {
			t.Errorf("metadata file produced a segment")
		}
		for _, s := range r.Snapshots {
			got = append(got, string(s.Content))
			if s.SessionID != sessionID || s.File != "subagents/agent-7.meta.json" || s.Capture != capture {
				t.Errorf("snapshot header = %s/%s/%+v", s.SessionID, s.File, s.Capture)
			}
		}
	}
	if want := []string{first, second, first}; !slices.Equal(got, want) {
		t.Errorf("snapshot contents = %q, want %q", got, want)
	}
	if r1.Snapshots[0].RecordID == r3.Snapshots[0].RecordID || r1.Snapshots[0].RecordID != r5.Snapshots[0].RecordID {
		t.Errorf("record ids do not follow content: %s %s %s", r1.Snapshots[0].RecordID, r3.Snapshots[0].RecordID, r5.Snapshots[0].RecordID)
	}
	if r5.File.Offset != int64(len(first)) || r5.File.Size != int64(len(first)) || r5.File.NextSegment != 0 {
		t.Errorf("metadata cursor = %+v", r5.File)
	}
}

func TestRead_MetaSnapshotWaitsForContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "empty file", content: ""},
		{name: "whitespace only", content: "\n"},
		{name: "half-written document", content: `{"agentType":"a",`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), sessionID, "subagents", "agent-1.meta.json")
			writeFile(t, path, tt.content)
			res, err := follow.Read(follow.File{Path: path}, capture, follow.ReadOptions{Root: root})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(res.Snapshots) != 0 {
				t.Errorf("got a snapshot of %q", tt.content)
			}
			if !tt.wantErr && res.File.Offset != int64(len(tt.content)) {
				t.Errorf("offset = %d, want %d", res.File.Offset, len(tt.content))
			}
		})
	}
}

func TestRead_SubagentFileNaming(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name        string
		path        string
		content     string
		wantSession string
		wantFile    string
	}{
		{name: "main file", path: filepath.Join(dir, sessionID+".jsonl"), content: userLine(1), wantSession: sessionID, wantFile: ""},
		{name: "agent lines", path: filepath.Join(dir, sessionID, "subagents", "agent-a1.jsonl"), content: userLine(1), wantSession: sessionID, wantFile: "subagents/agent-a1.jsonl"},
		{name: "agent metadata", path: filepath.Join(dir, sessionID, "subagents", "agent-a1.meta.json"), content: `{"agentType":"a"}`, wantSession: sessionID, wantFile: "subagents/agent-a1.meta.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeFile(t, tt.path, tt.content)
			res := read(t, follow.File{Path: tt.path})
			var gotSession, gotFile, gotID string
			switch {
			case len(res.Segments) == 1:
				s := res.Segments[0]
				gotSession, gotFile, gotID = s.SessionID, s.File, s.RecordID
				if s.SegmentIndex != 0 || s.Capture != capture || s.Lines != tt.content {
					t.Errorf("segment = %+v", s)
				}
				if want := envelope.SegmentRecordID(tt.wantSession, tt.wantFile, 0); gotID != want {
					t.Errorf("record id = %s, want %s", gotID, want)
				}
			case len(res.Snapshots) == 1:
				gotSession, gotFile = res.Snapshots[0].SessionID, res.Snapshots[0].File
			default:
				t.Fatalf("result = %+v, want one record", res)
			}
			if gotSession != tt.wantSession || gotFile != tt.wantFile {
				t.Errorf("session/file = %q/%q, want %q/%q", gotSession, gotFile, tt.wantSession, tt.wantFile)
			}
		})
	}
}

func TestSiblings(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  []string
	}{
		{name: "no session directory", setup: func(*testing.T, string) {}, want: []string{}},
		{name: "session directory without agents", setup: func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, sessionID, "tool-results", "toolu_1.txt"), "x")
		}, want: []string{}},
		{name: "agent files in their fixed place", setup: func(t *testing.T, dir string) {
			sub := filepath.Join(dir, sessionID, "subagents")
			writeFile(t, filepath.Join(sub, "agent-b.jsonl"), "")
			writeFile(t, filepath.Join(sub, "agent-a.meta.json"), "")
			writeFile(t, filepath.Join(sub, "agent-a.jsonl"), "")
			writeFile(t, filepath.Join(sub, "notes.txt"), "")
			writeFile(t, filepath.Join(sub, "other.jsonl"), "")
			writeFile(t, filepath.Join(sub, "agent-c.jsonl", "nested.jsonl"), "")
			writeFile(t, filepath.Join(dir, sessionID, "tool-results", "toolu_1.txt"), "x")
			writeFile(t, filepath.Join(dir, "other-session", "subagents", "agent-z.jsonl"), "")
		}, want: []string{
			filepath.Join("subagents", "agent-a.jsonl"),
			filepath.Join("subagents", "agent-a.meta.json"),
			filepath.Join("subagents", "agent-b.jsonl"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			got := follow.Siblings(filepath.Join(dir, sessionID+".jsonl"))
			want := make([]string, len(tt.want))
			for i, w := range tt.want {
				want[i] = filepath.Join(dir, sessionID, w)
			}
			if got == nil || !slices.Equal(got, want) {
				t.Errorf("Siblings() = %q, want %q", got, want)
			}
		})
	}
}

func TestRead_MalformedLineIsSkippedButConsumed(t *testing.T) {
	tests := []struct {
		name string
		line string
		kept bool
	}{
		{name: "plain text", line: "not json\n"},
		{name: "blank line", line: "\n"},
		{name: "json array", line: "[1,2]\n"},
		{name: "json null", line: "null\n"},
		{name: "json string", line: `"{}"` + "\n"},
		{name: "truncated object", line: `{"type":"user"` + "\n"},
		{name: "object with message that is a string", line: `{"type":"user","message":"hi"}` + "\n", kept: true},
		{name: "object with a numeric type", line: `{"type":7}` + "\n", kept: true},
		{name: "object with a numeric message id", line: `{"type":"assistant","message":{"id":7}}` + "\n", kept: true},
		{name: "empty object", line: "{}\n", kept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := mainPath(t)
			writeFile(t, path, userLine(1)+tt.line+userLine(2))
			res := read(t, follow.File{Path: path})
			if tt.kept {
				wantLines(t, res, userLine(1), tt.line, userLine(2))
			} else {
				wantLines(t, res, userLine(1), userLine(2))
			}
			wantCursor(t, res.File, int64(len(userLine(1)+tt.line+userLine(2))), 1)
		})
	}
}

func TestRead_LongLinesAreKeptWholeAndOversizedOnesAreConsumed(t *testing.T) {
	long := assistantLine("msg_long", 0, strings.Repeat("s", 300<<10))
	tests := []struct {
		name  string
		lines []string
		want  []string
		ids   []string
		short bool
	}{
		{name: "a line far past the read buffer", lines: []string{userLine(1), long, userLine(2)}, want: []string{userLine(1), long, userLine(2)}, ids: []string{"msg_long"}, short: true},
		{name: "a line past the line cap is consumed and dropped", lines: []string{userLine(1), oversizedLine(), userLine(2)}, want: []string{userLine(1), userLine(2)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if testing.Short() && !tt.short {
				t.Skip("skipped in short mode")
			}
			path := mainPath(t)
			content := strings.Join(tt.lines, "")
			writeFile(t, path, content)
			res := read(t, follow.File{Path: path})
			wantLines(t, res, tt.want...)
			wantCursor(t, res.File, int64(len(content)), 1, tt.ids...)
		})
	}
}

// oversizedLine is built without quoting: escaping 32 MiB would
// dominate the test time.
func oversizedLine() string {
	return strings.Replace(assistantLine("msg_huge", 0, ""), `"text":""`, `"text":"`+strings.Repeat("s", 32<<20)+`"`, 1)
}

func TestRead_NeverWritesTheFile(t *testing.T) {
	path := mainPath(t)
	content := strings.Join(fixtureLines(t), "") + "{\"type\":\"user\""
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	f := follow.File{Path: path}
	for range 3 {
		f = read(t, f).File
	}
	_ = read(t, follow.File{Path: path, Inode: 1, Size: int64(len(content)) + 1})

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Errorf("file changed: %v/%d -> %v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(content)) {
		t.Errorf("file content changed")
	}
}

func TestRead_StatFailureIsReported(t *testing.T) {
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	res, err := follow.Read(follow.File{Path: filepath.Join(path, "below-a-file")}, capture, follow.ReadOptions{Root: root})
	if err == nil {
		t.Fatalf("Read() = %+v, nil, want an error", res)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("err = %v, want a path error", err)
	}
}
