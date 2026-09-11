package lifecycle_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// signals is what the registry accumulated about root's session files.
func (e *env) signals(root string) proxytest.Signals {
	e.t.Helper()
	return e.sandbox.Signals(consent.ProjectIDHash(root))
}

// readerLog is every line the reader logged on this device, decoded.
func (e *env) readerLog() []map[string]any {
	e.t.Helper()
	data, err := os.ReadFile(e.layout().ReaderLog())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			e.t.Fatalf("reader log line %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// pausedBy is the build recorded with the standing pause.
func (e *env) pausedBy() string {
	e.t.Helper()
	return e.sandbox.PausedByBuild()
}

func TestReadSessionFiles_StopsAndPausesOnAnUnanchoredPathField(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"user","cwd":"/srv/work/sample","message":{"role":"user","content":"hi"},"someNewPath":"/srv/elsewhere/thing"}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())

	if got := e.storedRecords(); len(got) != 0 {
		t.Errorf("records = %d, want none stored from lines this build cannot mask", len(got))
	}
	if f := e.registeredFiles(root)[0]; f.Offset != 0 || f.NextSegment != 0 {
		t.Errorf("cursor = %+v, want left where it was", f)
	}
	if got := e.sandbox.PausedReason(); got != proxytest.PauseRedactionDrift {
		t.Errorf("PausedReason = %q, want %q", got, proxytest.PauseRedactionDrift)
	}
	if got := e.pausedBy(); got != e.deps.Version {
		t.Errorf("paused by build %q, want this build %q", got, e.deps.Version)
	}
	s := e.signals(root)
	if strings.Join(s.UnanchoredPathFields, ",") != "$.someNewPath" {
		t.Errorf("signals = %+v, want the field name recorded", s)
	}
	log := e.readerLog()
	if len(log) != 1 || log[0]["stop"] != true || log[0]["project_id_hash"] != consent.ProjectIDHash(root) {
		t.Fatalf("reader log = %v, want one stop line for the project", log)
	}
	raw, _ := os.ReadFile(e.layout().ReaderLog())
	for _, unwanted := range []string{"/srv/elsewhere", "0f1e2d3c", main} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("reader log = %s, want no %q", raw, unwanted)
		}
	}
}

func TestReadSessionFiles_RecordsAlertsWithoutStopping(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"assistant","cwd":"/srv/work/sample","apiBlockIndex":0,"message":{"id":"msg_1","role":"assistant","content":[]}}`+"\n"+
			`{"type":"assistant","cwd":"/srv/work/sample","apiBlockIndex":0,"message":{"role":"assistant","content":[]}}`+"\n"+
			`{"type":"mood-ring","mood":"calm"}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())

	if got := e.storedRecords(); len(got) != 1 {
		t.Errorf("records = %d, want the segment stored", len(got))
	}
	if f := e.registeredFiles(root)[0]; f.NextSegment != 1 {
		t.Errorf("cursor = %+v, want advanced past the segment", f)
	}
	if got := e.sandbox.PausedReason(); got != "" {
		t.Errorf("PausedReason = %q, want recording to go on", got)
	}
	s := e.signals(root)
	if s.AssistantLines != 2 || s.AssistantLinesWithoutMessageID != 1 || strings.Join(s.NewTopLevelTypes, ",") != "mood-ring" {
		t.Errorf("signals = %+v, want 1 of 2 assistant lines without an id and the new type", s)
	}
	if log := e.readerLog(); len(log) != 1 || log[0]["stop"] != nil || log[0]["assistant_lines_without_message_id"] != float64(1) {
		t.Errorf("reader log = %v, want one line with the count and no stop", log)
	}

	// The same lines read again on a rewrite are counted again: the
	// registry sums reads, it does not remember lines.
	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.signals(root); got.AssistantLines != 2 {
		t.Errorf("signals after a run over unchanged files = %+v, want unchanged", got)
	}
}

func TestReadSessionFiles_LinesOfTheExpectedShapeLeaveNoTrace(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"assistant","cwd":"/srv/work/sample","apiBlockIndex":0,"message":{"id":"msg_1","role":"assistant","content":[]}}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.signals(root); got.Any() {
		t.Errorf("signals = %+v, want nothing recorded for lines of the expected shape", got)
	}
	if _, err := os.Stat(e.layout().ReaderLog()); !os.IsNotExist(err) {
		t.Errorf("reader log exists (%v), want none written when nothing was found", err)
	}
}

func TestDoctor_ResumesRedactionPauseAfterUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pausedBy string
		resumed  bool
	}{
		{"a different build lifts the pause", "0.0.9", true},
		{"no recorded build lifts the pause", "", true},
		{"the same build leaves it standing", "testv", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, tc.pausedBy)

			problems, out := e.doctor()

			if got := e.sandbox.PausedReason(); (got == "") != tc.resumed {
				t.Errorf("PausedReason = %q, want resumed = %v", got, tc.resumed)
			}
			if tc.resumed {
				if !strings.Contains(out, "fixed: recording resumed after upgrade") || !strings.Contains(out, "this build is testv") {
					t.Errorf("doctor = %q, want the resume reported as fixed", out)
				}
				if strings.Contains(out, "trajector upgrade") {
					t.Errorf("doctor = %q, want no upgrade advice once resumed", out)
				}
			} else {
				if problems == 0 || !strings.Contains(out, "problem: recording is paused everywhere") || !strings.Contains(out, "`trajector upgrade`") {
					t.Errorf("doctor = %q (problems %d), want the standing pause reported with its way out", out, problems)
				}
			}
		})
	}
	t.Run("the other pause is not touched", func(t *testing.T) {
		e := newEnv(t)
		e.sandbox.Pause(proxytest.PauseConsentReconfirm)
		_, out := e.doctor()
		if got := e.sandbox.PausedReason(); got != proxytest.PauseConsentReconfirm {
			t.Errorf("PausedReason = %q, want the agreement pause left standing", got)
		}
		if strings.Contains(out, "resumed") {
			t.Errorf("doctor = %q, want nothing resumed", out)
		}
	})
}

func TestStatus_ShowsSignalCounts(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	root := e.canonicalRoot()
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-proj",
		ProjectIDHash: consent.ProjectIDHash(root),
		RootPath:      root,
		Upstream:      "https://api.anthropic.com",
		NoProxy:       true,
	})
	e.injectWithoutBaseURL()
	e.sandbox.AddSignals(consent.ProjectIDHash(root), proxytest.Signals{
		AssistantLines:                 7,
		AssistantLinesWithoutMessageID: 2,
		UnanchoredPathFields:           []string{"$.someNewPath"},
		NewTopLevelTypes:               []string{"mood-ring"},
	})

	out := e.statusOutput()
	if !strings.Contains(out, "2 of 7 assistant lines in this project's session files carried no message id.") {
		t.Errorf("status = %q, want the count stated", out)
	}
	if strings.Contains(out, "$.someNewPath") || strings.Contains(out, "mood-ring") {
		t.Errorf("status = %q, want no field names while recording is not paused for them, and never a logged value", out)
	}

	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, e.deps.Version)
	e.stdout.Reset()
	out = e.statusOutput()
	if !strings.Contains(out, "redaction does not cover: $.someNewPath.") {
		t.Errorf("status = %q, want the field named while paused for it", out)
	}
}
