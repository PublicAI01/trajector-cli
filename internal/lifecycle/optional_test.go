package lifecycle_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

const optionalKey = claudesettings.KeyShowThinkingSummaries

// containsWrapped reports whether out contains want regardless of where
// rendering wrapped its lines.
func containsWrapped(out, want string) bool {
	return strings.Contains(strings.Join(strings.Fields(out), " "), want)
}

func settingValue(t *testing.T, path string) (value, found bool) {
	t.Helper()
	return claudesettings.TopLevelBool(path, optionalKey)
}

func settingDecision(t *testing.T, e *env, hash string) (consent.SettingDecision, bool) {
	t.Helper()
	decisions, err := e.consentStore().SettingDecisions(hash)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := decisions[optionalKey]
	return d, ok
}

func writeUserSettings(t *testing.T, e *env, contents string) {
	t.Helper()
	path := claudesettings.UserSettingsPath(e.deps.Home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeProjectLocalSettings(t *testing.T, e *env, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.settingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.settingsPath(), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnableAsksAndWritesTheOptionalSettingOnYes(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\ny\n"

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
	}

	out := e.stdout.String()
	for _, want := range []string{
		"Optional setting for this project",
		"What changes",
		optionalKey + " becomes true in",
		"more valuable to us",
		"Claude Code stopped generating these summaries by default in",
		"Turn it on? [Y/n]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout misses %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "Turn it on?") > strings.Index(out, "Injected ") {
		t.Error("the question came after injection; it must come before")
	}
	if value, found := settingValue(t, e.settingsPath()); !found || !value {
		t.Errorf("setting = %v, %v after yes, want true", value, found)
	}
	d, ok := settingDecision(t, e, e.status().Hash)
	if !ok || d.Answer != consent.AnswerAccepted || d.Prior != consent.PriorAbsent {
		t.Errorf("decision = %+v, %v, want accepted with an absent prior", d, ok)
	}
}

func TestEnableEmptyInputTakesTheStatedDefault(t *testing.T) {
	cases := []struct {
		name       string
		seed       func(t *testing.T, e *env)
		wantPrompt string
		wantSaid   string
		wantOn     bool
		wantAnswer consent.SettingAnswer
	}{
		{
			name:       "an unset setting suggests yes",
			seed:       func(*testing.T, *env) {},
			wantPrompt: "Turn it on? [Y/n]",
			wantSaid:   "Optional setting for this project",
			wantOn:     true,
			wantAnswer: consent.AnswerAccepted,
		},
		{
			name: "a setting the user turned off suggests no",
			seed: func(t *testing.T, e *env) {
				writeUserSettings(t, e, `{"showThinkingSummaries": false}`)
			},
			wantPrompt: "Turn it on for this project? [y/N]",
			wantSaid:   "You have showThinkingSummaries set to false in your user settings.json.",
			wantOn:     false,
			wantAnswer: consent.AnswerDeclined,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			tc.seed(t, e)
			e.stdin = "yes\n\n"

			if err := e.machine().Enable(e.project, e.io()); err != nil {
				t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
			}
			out := e.stdout.String()
			if !strings.Contains(out, tc.wantPrompt) {
				t.Errorf("stdout misses %q:\n%s", tc.wantPrompt, out)
			}
			if !containsWrapped(out, tc.wantSaid) {
				t.Errorf("stdout misses %q:\n%s", tc.wantSaid, out)
			}
			if value, found := settingValue(t, e.settingsPath()); (found && value) != tc.wantOn {
				t.Errorf("setting = %v, %v, want on=%v", value, found, tc.wantOn)
			}
			if d, ok := settingDecision(t, e, e.status().Hash); !ok || d.Answer != tc.wantAnswer {
				t.Errorf("decision = %+v, %v, want %q", d, ok, tc.wantAnswer)
			}
		})
	}
}

func TestEnableDeclineIsRecordedAndRerunStillAsks(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\nn\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if value, found := settingValue(t, e.settingsPath()); found {
		t.Errorf("declining still wrote the setting: %v", value)
	}
	if d, ok := settingDecision(t, e, e.status().Hash); !ok || d.Answer != consent.AnswerDeclined {
		t.Errorf("decision = %+v, %v, want declined", d, ok)
	}

	e.stdout.Reset()
	e.stdin = "y\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "Turn it on? [Y/n]") {
		t.Errorf("rerun after a decline did not ask again:\n%s", e.stdout)
	}
	if value, found := settingValue(t, e.settingsPath()); !found || !value {
		t.Errorf("setting = %v, %v after changing the answer to yes, want true", value, found)
	}
}

func TestEnableLeavesAUsersOwnTrueAlone(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(t *testing.T, e *env)
		wantSaid string
		stillOn  func(t *testing.T, e *env) bool
	}{
		{
			name: "in the user settings",
			seed: func(t *testing.T, e *env) {
				writeUserSettings(t, e, `{"showThinkingSummaries": true}`)
			},
			wantSaid: "showThinkingSummaries is already true in your user settings.json.",
			stillOn: func(t *testing.T, e *env) bool {
				value, found := claudesettings.TopLevelBool(claudesettings.UserSettingsPath(e.deps.Home), optionalKey)
				return found && value
			},
		},
		{
			name: "in the project-local settings",
			seed: func(t *testing.T, e *env) {
				writeProjectLocalSettings(t, e, `{"showThinkingSummaries": true}`)
			},
			wantSaid: "showThinkingSummaries is already true in your project settings.local.json.",
			stillOn: func(t *testing.T, e *env) bool {
				value, found := settingValue(t, e.settingsPath())
				return found && value
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			tc.seed(t, e)

			if err := e.machine().Enable(e.project, e.io()); err != nil {
				t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
			}
			out := e.stdout.String()
			if !containsWrapped(out, tc.wantSaid) {
				t.Errorf("stdout misses %q:\n%s", tc.wantSaid, out)
			}
			if strings.Contains(out, "Turn it on?") || strings.Contains(out, "Turn it off?") {
				t.Errorf("a value the user set was questioned:\n%s", out)
			}
			if _, ok := settingDecision(t, e, e.status().Hash); ok {
				t.Error("a value the user set was recorded")
			}

			if err := e.machine().Disable(e.project, false, e.io()); err != nil {
				t.Fatalf("disable: %v", err)
			}
			if !tc.stillOn(t, e) {
				t.Error("disable removed a true the user set themselves")
			}
		})
	}
}

// editThenAnswer answers a prompt only after making an edit first, the
// way a user who changes a file while the question is on screen does.
type editThenAnswer struct {
	edit   func()
	answer io.Reader
	edited bool
}

func (r *editThenAnswer) Read(p []byte) (int, error) {
	if !r.edited {
		r.edited = true
		r.edit()
	}
	return r.answer.Read(p)
}

func TestEnableAcceptWhenAlreadyTrueRecordsNoWriteOfOurs(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\nn\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}

	in := &editThenAnswer{
		edit: func() {
			if err := claudesettings.SetTopLevelBool(e.settingsPath(), optionalKey, true); err != nil {
				t.Error(err)
			}
		},
		answer: strings.NewReader("y\n"),
	}
	if err := e.machine().Enable(e.project, lifecycle.IO{In: in, Out: e.stdout, Err: e.stderr}); err != nil {
		t.Fatalf("rerun: %v\nstdout: %s", err, e.stdout)
	}
	d, ok := settingDecision(t, e, e.status().Hash)
	if !ok || d.Answer != consent.AnswerAccepted || d.Prior != consent.PriorTrue {
		t.Errorf("decision = %+v, %v, want accepted with a true prior", d, ok)
	}

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if value, found := settingValue(t, e.settingsPath()); !found || !value {
		t.Errorf("disable touched a true trajector never wrote: %v, %v", value, found)
	}
}

func TestEnableRecordFailureLeavesTheSettingUnwritten(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\nn\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}

	in := &editThenAnswer{
		edit: func() {
			if err := os.WriteFile(e.deps.Layout.ConsentFile(), []byte("{ not json"), 0o600); err != nil {
				t.Error(err)
			}
		},
		answer: strings.NewReader("y\n"),
	}
	if err := e.machine().Enable(e.project, lifecycle.IO{In: in, Out: e.stdout, Err: e.stderr}); err != nil {
		t.Fatalf("rerun: %v\nstdout: %s\nstderr: %s", err, e.stdout, e.stderr)
	}
	if !strings.Contains(e.stderr.String(), "nothing was changed") {
		t.Errorf("stderr misses the degraded outcome:\n%s", e.stderr)
	}
	if value, found := settingValue(t, e.settingsPath()); found {
		t.Errorf("the setting was written without its record: %v", value)
	}
}

func TestEnableRerunStatesOurWriteAndKeepsItWithoutAnExplicitYes(t *testing.T) {
	for _, input := range []string{"\n", "n\n"} {
		t.Run(strings.TrimSuffix(input, "\n")+"<enter>", func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			e.stdin = "yes\ny\n"
			if err := e.machine().Enable(e.project, e.io()); err != nil {
				t.Fatalf("enable: %v", err)
			}

			e.stdout.Reset()
			e.stdin = input
			if err := e.machine().Enable(e.project, e.io()); err != nil {
				t.Fatalf("rerun: %v", err)
			}
			out := e.stdout.String()
			if !strings.Contains(out, "showThinkingSummaries is on for this project; trajector set it when") {
				t.Errorf("stdout misses the rerun statement:\n%s", out)
			}
			if !strings.Contains(out, "Turn it off? [y/N]") {
				t.Errorf("stdout misses the turn-off question:\n%s", out)
			}
			if value, found := settingValue(t, e.settingsPath()); !found || !value {
				t.Errorf("an answer short of an explicit yes turned the setting off: %v, %v", value, found)
			}
			if d, ok := settingDecision(t, e, e.status().Hash); !ok || d.Prior != consent.PriorAbsent {
				t.Errorf("decision = %+v, %v, want the original record kept", d, ok)
			}
		})
	}
}

func TestEnableRerunAnswerYesTurnsTheSettingBackOff(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\ny\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}

	e.stdout.Reset()
	e.stdin = "y\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "Set showThinkingSummaries back to what it was before trajector wrote it.") {
		t.Errorf("stdout misses the undo line:\n%s", e.stdout)
	}
	if _, found := settingValue(t, e.settingsPath()); found {
		t.Error("the key survived being turned back off")
	}
	if _, ok := settingDecision(t, e, e.status().Hash); ok {
		t.Error("the record survived a successful restore")
	}

	e.stdout.Reset()
	e.stdin = "n\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "Turn it on? [Y/n]") {
		t.Errorf("after turning it off the rerun did not ask afresh:\n%s", e.stdout)
	}
}

func TestDisableRestoresTheSettingToItsPriorState(t *testing.T) {
	cases := []struct {
		name      string
		seed      func(t *testing.T, e *env)
		wantAfter func(value, found bool) bool
	}{
		{
			name:      "an absent key is deleted",
			seed:      func(*testing.T, *env) {},
			wantAfter: func(_, found bool) bool { return !found },
		},
		{
			name: "an explicit false comes back",
			seed: func(t *testing.T, e *env) {
				writeProjectLocalSettings(t, e, `{"showThinkingSummaries": false}`)
			},
			wantAfter: func(value, found bool) bool { return found && !value },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			tc.seed(t, e)
			e.stdin = "yes\ny\n"
			if err := e.machine().Enable(e.project, e.io()); err != nil {
				t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
			}
			if value, found := settingValue(t, e.settingsPath()); !found || !value {
				t.Fatalf("test setup: setting = %v, %v after accepting, want true", value, found)
			}

			e.stdout.Reset()
			if err := e.machine().Disable(e.project, false, e.io()); err != nil {
				t.Fatalf("disable: %v", err)
			}
			if !strings.Contains(e.stdout.String(), "Set showThinkingSummaries back to what it was before trajector wrote it.") {
				t.Errorf("stdout misses the undo line:\n%s", e.stdout)
			}
			if value, found := settingValue(t, e.settingsPath()); !tc.wantAfter(value, found) {
				t.Errorf("setting after disable = %v, %v", value, found)
			}
			if _, ok := settingDecision(t, e, e.status().Hash); ok {
				t.Error("the record survived a successful restore")
			}
		})
	}
}

func TestDisableLeavesAHandEditedValueAlone(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\ny\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := claudesettings.SetTopLevelBool(e.settingsPath(), optionalKey, false); err != nil {
		t.Fatal(err)
	}

	e.stdout.Reset()
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if value, found := settingValue(t, e.settingsPath()); !found || value {
		t.Errorf("setting = %v, %v, want the hand-edited false kept", value, found)
	}
	if strings.Contains(e.stdout.String(), "Set showThinkingSummaries back") {
		t.Errorf("disable claims an undo it did not perform:\n%s", e.stdout)
	}
	if _, ok := settingDecision(t, e, e.status().Hash); ok {
		t.Error("the record outlived the decision it was for")
	}
}

func TestDisableKeepsTheRecordWhenRestoreFails(t *testing.T) {
	e := newEnv(t)
	hash := e.status().Hash
	consents := e.consentStore()
	if err := consents.SetProjectState(hash, e.canonicalRoot(), consent.StateGranted, "2026-08-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := consents.SetSettingDecision(hash, optionalKey, consent.SettingDecision{
		Answer: consent.AnswerAccepted, Prior: consent.PriorAbsent, DecidedAt: "2026-08-02T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	writeProjectLocalSettings(t, e, "{ not json")

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !strings.Contains(e.stderr.String(), "could not set showThinkingSummaries back") {
		t.Errorf("stderr misses the restore failure:\n%s", e.stderr)
	}
	if d, ok := settingDecision(t, e, hash); !ok || d.Answer != consent.AnswerAccepted {
		t.Errorf("decision = %+v, %v; a failed restore must keep its record", d, ok)
	}

	writeProjectLocalSettings(t, e, `{"showThinkingSummaries": true}`)
	e.stdout.Reset()
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if _, found := settingValue(t, e.settingsPath()); found {
		t.Error("the rerun did not finish the restore")
	}
	if _, ok := settingDecision(t, e, hash); ok {
		t.Error("the record survived the finished restore")
	}
}

func TestEnableNonInteractiveChangesNoOptionalSetting(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\n"

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
	}
	out := e.stdout.String()
	if !strings.Contains(out, "Optional settings were not changed: they need an interactive session.") ||
		!strings.Contains(out, "Run `trajector enable` from a terminal to review them.") {
		t.Errorf("stdout misses the non-interactive notice:\n%s", out)
	}
	if _, found := settingValue(t, e.settingsPath()); found {
		t.Error("a non-interactive enable wrote the setting")
	}
	if _, ok := settingDecision(t, e, e.status().Hash); ok {
		t.Error("a non-interactive enable recorded an answer nobody gave")
	}
}

func TestDisableLeavesDeclinedRecordsInPlace(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\nn\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if d, ok := settingDecision(t, e, e.status().Hash); !ok || d.Answer != consent.AnswerDeclined {
		t.Errorf("decision = %+v, %v; one refusal must survive disable", d, ok)
	}
}

func TestUninstallRestoresSettingsAcrossRoots(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	second := t.TempDir()
	e.stdin = "yes\ny\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable first project: %v", err)
	}
	e.stdin = "y\n"
	if err := e.machine().Enable(second, e.io()); err != nil {
		t.Fatalf("enable second project: %v", err)
	}
	secondRoot, err := consent.CanonicalRoot(second)
	if err != nil {
		t.Fatal(err)
	}
	secondSettings := claudesettings.ProjectLocalPath(secondRoot)
	if value, found := settingValue(t, secondSettings); !found || !value {
		t.Fatalf("test setup: second project setting = %v, %v, want true", value, found)
	}

	e.stdout.Reset()
	e.stdin = "no\n"
	if err := e.machine().Uninstall(false, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, found := settingValue(t, e.settingsPath()); found {
		t.Error("uninstall left trajector's write in the first project")
	}
	if _, found := settingValue(t, secondSettings); found {
		t.Error("uninstall left trajector's write in the second project")
	}
	if n := strings.Count(e.stdout.String(), "Set showThinkingSummaries back to what it was before trajector wrote it."); n != 2 {
		t.Errorf("undo line printed %d time(s), want one per project:\n%s", n, e.stdout)
	}
}

func TestDoctorCompletesTheWithdrawalOfAWrittenSetting(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "yes\ny\n"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	e.sandbox.RevokeProject(e.canonicalRoot(), "2026-08-21T00:00:00Z")

	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if _, found := settingValue(t, e.settingsPath()); found {
		t.Error("doctor removed the stale injection but left trajector's setting write")
	}
	if _, ok := settingDecision(t, e, e.status().Hash); ok {
		t.Error("the record survived doctor's restore")
	}
}
