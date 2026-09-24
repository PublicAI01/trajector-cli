package lifecycle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// jsonString is s as a session file carries it: a JSON string, so a
// Windows path's backslashes are escaped the way Claude Code writes
// them rather than read as escape sequences.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// signals is what the registry accumulated about root's session files.
func (e *env) signals(root string) proxytest.Signals {
	e.t.Helper()
	return e.sandbox.Signals(proxytest.ProjectIDHash(root))
}

// appendSessionLines adds lines to a session file already on disk,
// the way a running session gains them between two reads.
func (e *env) appendSessionLines(path, lines string) {
	e.t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := f.WriteString(lines); err != nil {
		f.Close()
		e.t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		e.t.Fatal(err)
	}
}

func TestReadSessionFiles_HoldsOnlyTheSegmentWhoseShapeIsNew(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	plain := `{"type":"user","cwd":"/srv/work/sample","message":{"role":"user","content":"hi"}}` + "\n"
	unanchored := `{"type":"user","cwd":"/srv/work/sample","message":{"role":"user","content":"hi"},"someNewPath":"/srv/work/sample/elsewhere/thing"}` + "\n"
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", plain)
	e.registerFile(root, main, "")

	// One read per segment: a read consumes what the file gained since
	// the cursor, so three reads over a growing file are three
	// segments, the middle one of a shape this build cannot mask.
	e.machine().ReadSessionFiles(e.project, discardIO())
	e.appendSessionLines(main, unanchored)
	e.machine().ReadSessionFiles(e.project, discardIO())
	e.appendSessionLines(main, plain)
	e.machine().ReadSessionFiles(e.project, discardIO())

	if got := e.storedRecords(); len(got) != 2 {
		t.Errorf("records = %d, want the two segments this build can mask", len(got))
	}
	held := e.sandbox.HeldRecords()
	if len(held) != 1 {
		t.Fatalf("held records = %d, want the one segment of a new shape", len(held))
	}
	if !strings.Contains(string(held[0].Raw), "someNewPath") {
		t.Errorf("held record = %s, want the segment that carries the new field", held[0].ID)
	}
	if f := e.registeredFiles(root)[0]; f.NextSegment != 3 || f.Offset != int64(len(plain)*2+len(unanchored)) {
		t.Errorf("cursor = %+v, want it past all three segments", f)
	}
	if got := e.sandbox.PausedReason(); got != "" {
		t.Errorf("PausedReason = %q, want recording to go on", got)
	}
	s := e.signals(root)
	if strings.Join(s.UnanchoredPathFields, ",") != "$.someNewPath" {
		t.Errorf("signals = %+v, want the field name recorded", s)
	}
	log := e.sandbox.ReaderLog()
	if len(log) != 1 || log[0].Stop || !log[0].Held || log[0].ProjectIDHash != proxytest.ProjectIDHash(root) {
		t.Fatalf("reader log = %+v, want one held entry for the project", log)
	}
	raw := e.sandbox.ReaderLogText()
	for _, unwanted := range []string{"/srv/work/sample", "0f1e2d3c", main} {
		if strings.Contains(raw, unwanted) {
			t.Errorf("reader log = %s, want no %q", raw, unwanted)
		}
	}
}

func TestStatusAndDoctorReportTheSegmentsHeldOnThisMachine(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"user","cwd":"/srv/work/sample","message":{"role":"user","content":"hi"},"someNewPath":"/srv/work/sample/elsewhere/thing"}`+"\n")
	e.registerFile(root, main, "")
	e.machine().ReadSessionFiles(e.project, discardIO())

	held := e.sandbox.HeldRecords()
	if len(held) != 1 {
		t.Fatalf("held records = %d, want the one segment of a new shape", len(held))
	}
	want := "1 segment(s) from 1 session(s) (" + platform.HumanBytes(held[0].Size) +
		") are held on this machine because their shape is new to this build; they are not uploaded"
	e.stdout.Reset()
	if _, err := e.machine().Status(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), want) {
		t.Errorf("status = %s, want the held line", e.stdout.String())
	}
	e.stdout.Reset()
	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), want) {
		t.Errorf("doctor = %s, want the held line", e.stdout.String())
	}
}

func TestForgetDeletesTheSegmentsHeldForThatSession(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/"+sessionOne+".jsonl",
		`{"type":"user","cwd":"/srv/work/sample","sessionId":"`+sessionOne+`","message":{"role":"user","content":"hi"},"someNewPath":"/srv/work/sample/elsewhere/thing"}`+"\n")
	e.registerFile(root, main, "")
	e.machine().ReadSessionFiles(e.project, discardIO())
	if len(e.sandbox.HeldRecords()) != 1 {
		t.Fatalf("held records = %d, want one before forgetting", len(e.sandbox.HeldRecords()))
	}

	if err := e.machine().Forget(sessionOne, e.io()); err != nil {
		t.Fatal(err)
	}

	if got := e.sandbox.HeldRecords(); len(got) != 0 {
		t.Errorf("held records = %d, want the session's held segments deleted", len(got))
	}
}

func TestDoctorUploadsAHeldSegmentOnceTheBuildKnowsItsShape(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"user","cwd":"/srv/work/sample","message":{"role":"user","content":"hi"},"someNewPath":`+jsonString(t, filepath.Join(e.deps.Home, "notes", "plan.md"))+`}`+"\n")
	e.registerFile(root, main, "")
	e.machine().ReadSessionFiles(e.project, discardIO())
	if len(e.sandbox.HeldRecords()) != 1 {
		t.Fatalf("held records = %d, want the segment held by the reading build", len(e.sandbox.HeldRecords()))
	}

	// A build that does not report this shape stands in for the one
	// that covers it: doctor reads the held segments through its own
	// detector, not through the one that held them.
	e.deps.Home = filepath.Join(e.deps.Home, "moved")
	e.stdout.Reset()
	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	if got := e.sandbox.HeldRecords(); len(got) != 0 {
		t.Errorf("held records = %d, want the segment released", len(got))
	}
	if got := e.storedRecords(); len(got) != 1 {
		t.Errorf("records = %d, want the released segment waiting for upload", len(got))
	}
	if !strings.Contains(e.stdout.String(), "1 held segment(s) read cleanly under this build") {
		t.Errorf("doctor = %s, want it to say what it released", e.stdout.String())
	}
}

func TestDoctorKeepsHoldingASegmentWhoseLinesNameTheProjectWithoutACwd(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"summary","summary":"done","someNewPath":`+jsonString(t, filepath.Join(root, "x.bak"))+`}`+"\n")
	e.registerFile(root, main, "")
	e.machine().ReadSessionFiles(e.project, discardIO())
	if len(e.sandbox.HeldRecords()) != 1 {
		t.Fatalf("held records = %d, want the segment held by the reading build", len(e.sandbox.HeldRecords()))
	}

	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	if got := e.sandbox.HeldRecords(); len(got) != 1 {
		t.Errorf("held records = %d, want the segment still held", len(got))
	}
	if got := e.storedRecords(); len(got) != 0 {
		t.Errorf("records = %d, want nothing this build cannot mask waiting for upload", len(got))
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, tc.pausedBy)

			_, out := e.doctor()

			if got := e.sandbox.PausedReason(); (got == "") != tc.resumed {
				t.Errorf("PausedReason = %q, want resumed = %v", got, tc.resumed)
			}
			if !strings.Contains(out, "fixed: recording resumed after upgrade") || !strings.Contains(out, "this build is testv") {
				t.Errorf("doctor = %q, want the resume reported as fixed", out)
			}
			if strings.Contains(out, "trajector upgrade") {
				t.Errorf("doctor = %q, want no upgrade advice once resumed", out)
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
		IncompleteSegments:                  1,
	})

	out := e.statusOutput()
	if !strings.Contains(out, "2 of 7 assistant lines in this project's session files carried no message id.") {
		t.Errorf("status = %q, want the count stated", out)
	}
	if !strings.Contains(out, "3 of 7 assistant lines in this project's session files lacked a response field a record is read by.") {
		t.Errorf("status = %q, want every count the registry holds stated", out)
	}
	if strings.Contains(out, "$.someNewPath") || strings.Contains(out, "mood-ring") || strings.Contains(out, "without a newline") {
		t.Errorf("status = %q, want no field names and no pause cause while recording is not paused, and never a logged value", out)
	}

	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, e.deps.Version)
	e.stdout.Reset()
	out = e.statusOutput()
	if !strings.Contains(out, "1 read(s) of this project's session files ended in a line without a newline.") {
		t.Errorf("status = %q, want the reads that paused recording counted", out)
	}
	if strings.Contains(out, "$.someNewPath") {
		t.Errorf("status = %q, want no field named under the pause: a field only holds back its own segment", out)
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
			if err := e.machine().Enable(e.project, choices(proxytest.WithProxy), e.io()); err != nil {
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

func TestDoctorResumesARedactionPauseThisBuildSetOnceItReadsTheFilesCleanly(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl",
		`{"type":"user","cwd":"/srv/work/sample","message":{"role":"user","content":"hi"}}`+"\n")
	e.registerFile(root, main, "")
	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, e.deps.Version)

	e.stdout.Reset()
	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	if got := e.sandbox.PausedReason(); got != "" {
		t.Errorf("PausedReason = %q, want the pause lifted by the build that set it", got)
	}
	const want = "recording resumed: this build read the session files again and every line it read ended in a newline"
	if !strings.Contains(e.stdout.String(), want) {
		t.Errorf("doctor = %s, want %q", e.stdout.String(), want)
	}
	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Errorf("records = %d, want reading to go on after the pause was lifted", len(got))
	}
}
