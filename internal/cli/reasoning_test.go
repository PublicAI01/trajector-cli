package cli_test

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

const (
	cliLine     = "trajector: no reasoning summaries in this session; run trajector enable, then start a new session\n"
	desktopLine = "trajector: no reasoning summaries; set Default transcript view to Thinking, then start a new session\n"
	sdkLine     = "trajector: no reasoning summaries in this session; add --thinking-display summarized and rerun\n"
	unknownLine = "trajector: no reasoning summaries in this session; run trajector status to see how to turn them on\n"
	recordsNone = "trajector: nothing of this session is being recorded; run trajector status\n"
)

// reasoningEnv is a project enabled without the proxy, whose session
// files a test writes and then has read, as a session's reader would.
type reasoningEnv struct {
	*clitest.Env
	t *testing.T
}

func newReasoningEnv(t *testing.T) *reasoningEnv {
	t.Helper()
	e := clitest.New(t)
	e.Paired()
	e.At(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))
	earlierSessionFile(t, e, "0e0e0e0e")
	if got := e.InProjectInput("yes\n", "enable", "--no-proxy"); got.Exit != 0 {
		t.Fatalf("enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
	}
	return &reasoningEnv{Env: e, t: t}
}

// session writes a session file named sid in the project's session
// directory holding lines, and returns its path.
func (r *reasoningEnv) session(sid string, lines ...string) string {
	r.t.Helper()
	path := filepath.Join(r.Home(), "claude", "projects", discover.Encode(r.ProjectRoot()), sid+".jsonl")
	r.write(path, lines...)
	return path
}

func (r *reasoningEnv) write(path string, lines ...string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// hook runs the progress hook for the session at path, as event.
func (r *reasoningEnv) hook(event, path string) clitest.Result {
	r.t.Helper()
	data, err := json.Marshal(map[string]string{
		"session_id":      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		"transcript_path": path,
		"cwd":             r.Project(),
		"hook_event_name": event,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return r.InProjectInput(string(data), "hook", "progress")
}

// readFiles reads what the project's session files gained, as the
// reader a hook hands its work to does.
func (r *reasoningEnv) readFiles() {
	r.t.Helper()
	if got := r.Run("hook", "read", r.Project()); got.Exit != 0 {
		r.t.Fatalf("hook read = exit %d, stderr: %s", got.Exit, got.Stderr)
	}
}

// registerAndRead has the session at path registered by a hook of its
// own and then read, so the next hook sees what it holds.
func (r *reasoningEnv) registerAndRead(path string) {
	r.t.Helper()
	if got := r.hook("Stop", path); got.Exit != 0 || got.Stderr != "" {
		r.t.Fatalf("first hook of the session = exit %d, stderr %q; want silence", got.Exit, got.Stderr)
	}
	r.readFiles()
}

// thinking is an assistant line of message id with one reasoning
// block holding text, from a session started at surface. An empty
// surface leaves the field out.
func thinking(id, text, surface string) string {
	entrypoint := ""
	if surface != "" {
		entrypoint = fmt.Sprintf(`"entrypoint":%q,`, surface)
	}
	return fmt.Sprintf(`{"type":"assistant",%s"message":{"id":%q,"role":"assistant","content":[{"type":"thinking","thinking":%q,"signature":"sig"}]}}`, entrypoint, id, text)
}

func wantSaid(t *testing.T, got clitest.Result, line string) {
	t.Helper()
	if got.Exit != 1 || got.Stderr != line || got.Stdout != "" {
		t.Errorf("exit = %d, stdout = %q, stderr = %q; want exit 1 and only %q on stderr", got.Exit, got.Stdout, got.Stderr, line)
	}
}

func TestHookProgress_TellsASessionWhoseReasoningMostlyHoldsNoTextHowToTurnSummariesOn(t *testing.T) {
	tests := []struct {
		surface string
		want    string
	}{
		{surface: "cli", want: cliLine},
		{surface: "claude-desktop", want: desktopLine},
		{surface: "claude-desktop-3p", want: desktopLine},
		{surface: "sdk-cli", want: sdkLine},
		{surface: "sdk-ts", want: sdkLine},
		{surface: "sdk-py", want: sdkLine},
		{surface: "claude-vscode", want: unknownLine},
		{surface: "", want: unknownLine},
	}
	for _, tt := range tests {
		t.Run(cmp.Or(tt.surface, "no launch surface"), func(t *testing.T) {
			r := newReasoningEnv(t)
			path := r.session("1a1a1a1a",
				thinking("msg_1", "", tt.surface),
				thinking("msg_2", "", tt.surface),
				thinking("msg_3", "weighing", tt.surface),
			)
			r.registerAndRead(path)

			got := r.hook("Stop", path)
			wantSaid(t, got, tt.want)
			if n := utf8.RuneCountInString(strings.TrimSuffix(got.Stderr, "\n")); n > 100 {
				t.Errorf("line is %d runes, want at most 100", n)
			}
		})
	}
}

func TestHookProgress_SaysNothingWhileMostReasoningHoldsText(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
	}{
		{name: "more with text", lines: []string{thinking("msg_1", "", "cli"), thinking("msg_2", "a", "cli"), thinking("msg_3", "b", "cli")}},
		{name: "as many with text", lines: []string{thinking("msg_1", "", "cli"), thinking("msg_2", "a", "cli")}},
		{name: "no reasoning at all", lines: []string{`{"type":"assistant","entrypoint":"cli","message":{"id":"msg_1","content":[{"type":"text","text":"done"}]}}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReasoningEnv(t)
			path := r.session("1a1a1a1a", tt.lines...)
			r.registerAndRead(path)

			assertSilentSuccess(t, r.hook("Stop", path))
		})
	}
}

func TestHookProgress_LeavesTheReasoningOfTheSessionsAgentFilesOut(t *testing.T) {
	r := newReasoningEnv(t)
	path := r.session("1a1a1a1a", thinking("msg_1", "a", "cli"))
	r.write(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "agent-a1.jsonl"),
		thinking("msg_2", "", ""), thinking("msg_3", "", ""))
	r.registerAndRead(path)

	assertSilentSuccess(t, r.hook("Stop", path))
}

func TestHookProgress_TellsAProjectOncePerUTCDay(t *testing.T) {
	r := newReasoningEnv(t)
	first := r.session("1a1a1a1a", thinking("msg_1", "", "cli"))
	r.registerAndRead(first)
	wantSaid(t, r.hook("Stop", first), cliLine)
	assertSilentSuccess(t, r.hook("Stop", first))

	r.At(time.Date(2026, 10, 13, 23, 59, 0, 0, time.UTC))
	second := r.session("2b2b2b2b", thinking("msg_2", "", "claude-desktop"))
	r.registerAndRead(second)
	assertSilentSuccess(t, r.hook("Stop", second))

	r.At(time.Date(2026, 10, 14, 0, 1, 0, 0, time.UTC))
	wantSaid(t, r.hook("Stop", second), desktopLine)
	assertSilentSuccess(t, r.hook("Stop", first))
}

func TestHookProgress_ASubagentStopSaysNothingAndLeavesTheLineForTheSessionsTurn(t *testing.T) {
	r := newReasoningEnv(t)
	path := r.session("1a1a1a1a", thinking("msg_1", "", "cli"))
	r.registerAndRead(path)

	assertSilentSuccess(t, r.hook("SubagentStop", path))
	wantSaid(t, r.hook("PostToolBatch", path), cliLine)
}

func TestHookProgress_ADeviceThatRecordsNothingSaysOnlyThatAndNothingOfReasoning(t *testing.T) {
	r := newReasoningEnv(t)
	path := r.session("1a1a1a1a", thinking("msg_1", "", "cli"))
	r.registerAndRead(path)
	r.Sandbox().Pause(proxytest.PauseSignedOut)

	wantSaid(t, r.hook("Stop", path), recordsNone)
	assertSilentSuccess(t, r.hook("Stop", path))
	r.Sandbox().Resume(proxytest.PauseSignedOut)
	wantSaid(t, r.hook("Stop", path), cliLine)
}

func TestHook_SessionStartAndEndNeverTellAboutReasoning(t *testing.T) {
	for _, hook := range []string{"ensure-proxy", "session-end"} {
		t.Run(hook, func(t *testing.T) {
			r := newReasoningEnv(t)
			path := r.session("1a1a1a1a", thinking("msg_1", "", "cli"))
			r.registerAndRead(path)
			data, err := json.Marshal(map[string]string{
				"session_id":      "1a1a1a1a",
				"transcript_path": path,
				"cwd":             r.Project(),
				"hook_event_name": "SessionStart",
			})
			if err != nil {
				t.Fatal(err)
			}

			assertSilentSuccess(t, r.InProjectInput(string(data), "hook", hook))
			wantSaid(t, r.hook("Stop", path), cliLine)
		})
	}
}
