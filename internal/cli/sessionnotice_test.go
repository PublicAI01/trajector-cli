package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

const nothingRecorded = "trajector: nothing of this session is being recorded; run trajector status"

// toldUser returns the message a hook gave Claude Code to show the
// user. A message is one JSON object on stdout with systemMessage as
// its only field, from a hook that exits 0 and says nothing on stderr:
// Claude Code shows a non-zero exit as a hook error, and plain text on
// stdout reaches the model.
func toldUser(t *testing.T, got clitest.Result) string {
	t.Helper()
	if got.Exit != 0 || got.Stderr != "" {
		t.Errorf("exit = %d, stderr = %q; want exit 0 and nothing on stderr", got.Exit, got.Stderr)
	}
	if !strings.HasPrefix(got.Stdout, "{") || strings.Count(got.Stdout, "\n") != 1 || !strings.HasSuffix(got.Stdout, "}\n") {
		t.Fatalf("stdout = %q, want one JSON object on one line", got.Stdout)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(got.Stdout), &fields); err != nil {
		t.Fatalf("stdout = %q: %v", got.Stdout, err)
	}
	message, ok := fields["systemMessage"].(string)
	if !ok || len(fields) != 1 {
		t.Fatalf("stdout = %q, want systemMessage as the only field", got.Stdout)
	}
	return message
}

func TestHook_SessionStartTellsAStoppedDeviceAsAMessageForTheUser(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")
	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "session-end"))
	h.Sandbox().Pause(proxytest.PauseSignedOut)

	if got := toldUser(t, h.InProjectInput(h.input(main), "hook", "ensure-proxy")); got != nothingRecorded {
		t.Errorf("message = %q, want %q", got, nothingRecorded)
	}
	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "ensure-proxy"))
}

func TestHook_SessionStartPutsWhatItSaidBeforeTheNotice(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")
	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "session-end"))
	h.Sandbox().AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z")

	got := toldUser(t, h.InProjectInput(h.input(main), "hook", "ensure-proxy"))
	want := "trajector: " + routing.PauseConsentReconfirm.Explain() + "\n" + nothingRecorded
	if got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestHook_ProgressTellsAStoppedDeviceAsAMessageForTheUserOnEveryTurnEvent(t *testing.T) {
	for _, event := range []string{"Stop", "PostToolBatch"} {
		t.Run(event, func(t *testing.T) {
			r := newReasoningEnv(t)
			path := r.session("1a1a1a1a", thinking("msg_1", "a", "cli"))
			r.registerAndRead(path)
			r.Sandbox().Pause(proxytest.PauseSignedOut)

			if got := toldUser(t, r.hook(event, path)); got != nothingRecorded {
				t.Errorf("message = %q, want %q", got, nothingRecorded)
			}
		})
	}
}

func TestHook_SessionEndTellsAStoppedDeviceOnStderr(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")
	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "session-end"))
	h.Sandbox().Pause(proxytest.PauseSignedOut)

	got := h.InProjectInput(h.input(main), "hook", "session-end")
	if got.Exit != 1 || got.Stderr != nothingRecorded+"\n" || got.Stdout != "" {
		t.Errorf("exit = %d, stdout = %q, stderr = %q; want exit 1 and only the notice on stderr", got.Exit, got.Stdout, got.Stderr)
	}
}

func TestHook_DiscoveryTellsAProjectNotEnabledOnceAsAMessageForTheUser(t *testing.T) {
	h := newHookEnv(t)

	got := toldUser(t, h.InProject("hook", "discovery"))
	if !strings.Contains(got, "this project is not contributing coding data") || !strings.Contains(got, "`trajector enable`") {
		t.Errorf("message = %q, want the hint that names trajector enable", got)
	}
	assertSilentSuccess(t, h.InProject("hook", "discovery"))
}
