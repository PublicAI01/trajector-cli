package follow_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/sessionline"
)

// reasoningLine is an assistant line of message id whose one block
// carries the reasoning field holding text, or nothing when text is
// empty, from a session started at surface.
func reasoningLine(id, text, surface string) string {
	return fmt.Sprintf(`{"type":"assistant","entrypoint":%q,"cwd":%q,"sessionId":%q,"message":{"id":%q,"role":"assistant","content":[{"type":"thinking","thinking":%q,"signature":"sig"}]}}`+"\n", surface, root, sessionID, id, text)
}

func wantReasoning(t *testing.T, f follow.File, want sessionline.ReasoningBlocks, surface string) {
	t.Helper()
	if got := f.Reasoning(); got != want {
		t.Errorf("reasoning counts = %+v, want %+v", got, want)
	}
	if f.LaunchSurface != surface {
		t.Errorf("launch surface = %q, want %q", f.LaunchSurface, surface)
	}
}

func TestRead_CountsReasoningBlocksOfKeptAssistantLinesAcrossReads(t *testing.T) {
	path := mainPath(t)
	writeFile(t, path, reasoningLine("msg_a", "", "cli")+reasoningLine("msg_b", "weighing", "cli")+userLine(1))
	first := read(t, follow.File{Path: path})
	wantReasoning(t, first.File, sessionline.ReasoningBlocks{Empty: 1, WithText: 1}, "cli")

	appendFile(t, path, reasoningLine("msg_c", "", "claude-desktop")+assistantLine("msg_d", 0, "done"))
	second := read(t, first.File)
	wantReasoning(t, second.File, sessionline.ReasoningBlocks{Empty: 2, WithText: 1}, "claude-desktop")
}

func TestRead_ReasoningFieldOnALineOfAnotherTypeIsNotCounted(t *testing.T) {
	path := mainPath(t)
	writeFile(t, path, `{"type":"user","entrypoint":"sdk-cli","message":{"id":"msg_u","content":[{"type":"thinking","thinking":""}]}}`+"\n")
	res := read(t, follow.File{Path: path})
	wantReasoning(t, res.File, sessionline.ReasoningBlocks{}, "sdk-cli")
}

func TestRead_LinesNotKeptAreNotCounted(t *testing.T) {
	t.Run("written outside consent", func(t *testing.T) {
		path := mainPath(t)
		writeFile(t, path, reasoningLine("msg_a", "x", "cli")+relocatedLine("/elsewhere")+reasoningLine("msg_b", "", "sdk-cli"))
		res := read(t, follow.File{Path: path})
		wantReasoning(t, res.File, sessionline.ReasoningBlocks{WithText: 1}, "cli")
	})
	t.Run("written before a move into consent", func(t *testing.T) {
		path := mainPath(t)
		writeFile(t, path, reasoningLine("msg_a", "", "sdk-cli")+relocatedLine(root)+reasoningLine("msg_b", "x", "cli"))
		res := read(t, follow.File{Path: path})
		wantReasoning(t, res.File, sessionline.ReasoningBlocks{WithText: 1}, "cli")
	})
	t.Run("consumed before a rewrite", func(t *testing.T) {
		path := mainPath(t)
		writeFile(t, path, reasoningLine("msg_a", "", "cli"))
		first := read(t, follow.File{Path: path})
		replaceFile(t, path, reasoningLine("msg_a", "", "cli")+reasoningLine("msg_b", "x", "cli"))
		second := read(t, first.File)
		if second.Reaction != follow.Rewrite {
			t.Fatalf("Reaction = %v, want Rewrite", second.Reaction)
		}
		wantReasoning(t, second.File, sessionline.ReasoningBlocks{Empty: 1, WithText: 1}, "cli")
	})
}

func TestReader_PersistsReasoningCountsWithTheCursorAndKeepsTheHooksFields(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, reasoningLine("msg_a", "", "cli"))
	a.register(path, "")
	if _, err := a.registry.Tell(project, path); err != nil {
		t.Fatal(err)
	}

	a.reader.Advance(path)
	appendFile(t, path, reasoningLine("msg_b", "", "cli")+reasoningLine("msg_c", "x", "cli"))
	a.reader.Advance(path)

	got := a.entry(path)
	wantReasoning(t, got, sessionline.ReasoningBlocks{Empty: 2, WithText: 1}, "cli")
	if !got.Told {
		t.Errorf("the session's told mark was lost when the reader wrote its cursor")
	}
	stored := a.stored()
	for key, want := range map[string]string{"empty_reasoning_blocks": "2", "reasoning_blocks_with_text": "1", "launch_surface": `"cli"`} {
		if got := string(stored[0][key]); got != want {
			t.Errorf("stored %s = %s, want %s", key, got, want)
		}
	}
}

func TestReader_AStoreThatKeepsNothingLeavesTheCountsAsTheyWere(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, reasoningLine("msg_a", "", "cli"))
	a.register(path, "")
	a.answer = follow.NotStored

	a.reader.Advance(path)

	wantReasoning(t, a.entry(path), sessionline.ReasoningBlocks{}, "")
}

func TestRegistry_TellReasoningOncePerProjectAndUTCDay(t *testing.T) {
	a := newAdvancing(t)
	a.register(mainPath(t), "")
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	steps := []struct {
		at   string
		want bool
	}{
		{at: "2026-10-13T08:00:00Z", want: true},
		{at: "2026-10-13T23:59:59Z", want: false},
		// The same instant as the one before, read in another zone: the
		// day is the UTC one.
		{at: "2026-10-14T01:59:59+02:00", want: false},
		{at: "2026-10-14T00:00:00Z", want: true},
		{at: "2026-10-14T12:00:00Z", want: false},
	}
	for _, s := range steps {
		first, err := a.registry.TellReasoning(project, at(s.at))
		if err != nil || first != s.want {
			t.Errorf("TellReasoning at %s = %v, %v; want %v", s.at, first, err, s.want)
		}
	}
	raw, err := os.ReadFile(filepath.Join(a.dir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Day string `json:"reasoning_told_on"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil || reg.Day != "2026-10-14" {
		t.Errorf("stored reasoning_told_on = %q (%v), want 2026-10-14", reg.Day, err)
	}
}

func TestRegistry_TellReasoningRefusesAProjectWithoutARegistry(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(dir string)
	}{
		{name: "no directory", setup: func(string) {}},
		{name: "no registry file", setup: func(dir string) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "follow")
			tt.setup(dir)
			first, err := follow.Open(dir).TellReasoning(project, time.Now())
			if first || !errors.Is(err, follow.ErrNotRegistered) {
				t.Errorf("TellReasoning = %v, %v; want false, ErrNotRegistered", first, err)
			}
			if _, err := os.Stat(filepath.Join(dir, project+".json")); !os.IsNotExist(err) {
				t.Errorf("a registry was created (stat: %v)", err)
			}
		})
	}
}
