package lifecycle_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// signals is what the registry accumulated about root's session files.
func (e *env) signals(root string) proxytest.Signals {
	e.t.Helper()
	return e.sandbox.Signals(proxytest.ProjectIDHash(root))
}

func TestReadSessionFiles_StopsAndPausesOnAnUnanchoredPathField(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"user","cwd":"/srv/work/sample","message":{"role":"user","content":"hi"},"someNewPath":"/srv/work/sample/elsewhere/thing"}`+"\n")
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
	if e.sandbox.ResumeOtherBuild(proxytest.PauseRedactionDrift, e.deps.Version) {
		t.Errorf("this build lifted the pause it set, want it attributed to build %q", e.deps.Version)
	}
	s := e.signals(root)
	if strings.Join(s.UnanchoredPathFields, ",") != "$.someNewPath" {
		t.Errorf("signals = %+v, want the field name recorded", s)
	}
	log := e.sandbox.ReaderLog()
	if len(log) != 1 || !log[0].Stop || log[0].ProjectIDHash != proxytest.ProjectIDHash(root) {
		t.Fatalf("reader log = %+v, want one stop entry for the project", log)
	}
	raw := e.sandbox.ReaderLogText()
	for _, unwanted := range []string{"/srv/work/sample", "0f1e2d3c", main} {
		if strings.Contains(raw, unwanted) {
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
	if log := e.sandbox.ReaderLog(); len(log) != 1 || log[0].Stop || log[0].AssistantLinesWithoutMessageID != 1 {
		t.Errorf("reader log = %+v, want one entry with the count and no stop", log)
	}

	// The same lines read again on a rewrite are counted again: the
	// registry sums reads, it does not remember lines.
	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.signals(root); got.AssistantLines != 2 {
		t.Errorf("signals after a run over unchanged files = %+v, want unchanged", got)
	}
}

func TestReadSessionFiles_CountsLinesCarryingNoReasoningWithoutLoggingThem(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"assistant","cwd":"/srv/work/sample","apiBlockIndex":0,"message":{"id":"msg_1","role":"assistant","content":[{"type":"thinking","thinking":""}]}}`+"\n"+
			`{"type":"assistant","cwd":"/srv/work/sample","apiBlockIndex":0,"message":{"id":"msg_2","role":"assistant","content":[{"type":"thinking","thinking":""}]}}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())

	if got := e.storedRecords(); len(got) != 1 {
		t.Errorf("records = %d, want the segment stored", len(got))
	}
	s := e.signals(root)
	if s.AssistantLines != 2 || s.AssistantLinesWithEmptyReasoning != 2 {
		t.Errorf("signals = %+v, want both lines counted", s)
	}
	if got := e.sandbox.ReaderLogText(); got != "" {
		t.Errorf("reader log = %q, want no line for a shape this build expects to meet", got)
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
	if got := e.sandbox.ReaderLogText(); got != "" {
		t.Errorf("reader log = %q, want nothing written when nothing was found", got)
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
		ProjectIDHash: proxytest.ProjectIDHash(root),
		RootPath:      root,
		Upstream:      "https://api.anthropic.com",
		Shape:         proxytest.WithoutProxy,
	})
	e.injectWithoutBaseURL()
	e.sandbox.AddSignals(proxytest.ProjectIDHash(root), proxytest.Signals{
		AssistantLines:                      7,
		AssistantLinesWithoutMessageID:      2,
		AssistantLinesMissingResponseFields: 3,
		UnanchoredPathFields:                []string{"$.someNewPath"},
		NewTopLevelTypes:                    []string{"mood-ring"},
	})

	out := e.statusOutput()
	if !strings.Contains(out, "2 of 7 assistant lines in this project's session files carried no message id.") {
		t.Errorf("status = %q, want the count stated", out)
	}
	if !strings.Contains(out, "3 of 7 assistant lines in this project's session files lacked a response field a record is read by.") {
		t.Errorf("status = %q, want every count the registry holds stated", out)
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

func TestStatus_PairsTheEmptyReasoningCountWithTheSettingThatFillsIt(t *testing.T) {
	const fact = "6 of 9 assistant lines carry no reasoning."
	const wayOut = optionalKey + " is off for this project; run `trajector enable` to turn it on."
	for _, tc := range []struct {
		name    string
		stdin   string
		wantWay bool
	}{
		{name: "nothing answered, so the setting stays off", stdin: "yes\n", wantWay: true},
		{name: "accepted, so trajector set it on", stdin: "yes\ny\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			e.stdin = tc.stdin
			if err := e.machine().Enable(e.project, proxytest.WithProxy, e.io()); err != nil {
				t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
			}
			e.sandbox.AddSignals(proxytest.ProjectIDHash(e.canonicalRoot()), proxytest.Signals{
				AssistantLines:                   9,
				AssistantLinesWithEmptyReasoning: 6,
			})
			e.stdout.Reset()

			out := e.statusOutput()

			if !strings.Contains(out, fact) {
				t.Errorf("status = %q, want it to contain %q", out, fact)
			}
			if got := strings.Contains(out, wayOut); got != tc.wantWay {
				t.Errorf("status = %q, want the way out present = %v", out, tc.wantWay)
			}
		})
	}
}
