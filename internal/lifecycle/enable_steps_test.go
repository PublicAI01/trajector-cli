package lifecycle_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

const (
	deviceWideTermsLine  = "You are accepting these terms for this device, not only for this project. Any project you enable on this device is covered."
	hooksWillNotLoadLine = "Judged from configuration readable on this machine, Claude Code will not load trajector's hooks in this project"
	proxyHalfOnlyLine    = "Only the proxy records this project for now; its session files are not read"
	noProxyNothingLine   = "Judged from configuration readable on this machine, Claude Code will not run trajector's hooks here, so --no-proxy would record nothing from this project. Either accept that nothing is recorded for now, or run trajector enable without --no-proxy (the proxy records; /remote-control inside this project becomes unavailable, claude remote-control still works)."
	enableAnywayPrompt   = "Enable anyway? [y/N]"
	remoteControlLine    = "Remote Control: inside this project, /remote-control will not be available. To use it, either start sessions with claude remote-control (both sources are still recorded), or run trajector enable --no-proxy to record only the session files (Remote Control stays available; records from one source may be rewarded differently)."
	noProxyFactLine      = "This project records from its session files only, so Remote Control stays available."
	contributesLine      = "This project now contributes data."
	agreementPrompt      = "Do you accept the data agreement? [yes/no]:"
)

func earlierSessionsLine(n int, oldest time.Time) string {
	if n == 0 {
		return "No earlier session records to collect."
	}
	local := oldest.Local()
	return fmt.Sprintf("%d earlier session record(s) will be collected once; the oldest is from %s.", n, local.Format("2006-01-02"))
}

func truncatedLine() string {
	return fmt.Sprintf("This project's directory tree has more than %d directories, so the count above is incomplete.", discover.Limit)
}

// sessionFile writes a session file where Claude Code would keep one
// for this project, with the given modification time.
func (e *env) sessionFile(sid string, mtime time.Time) string {
	e.t.Helper()
	dir := filepath.Join(e.sessionFilesRoot(), discover.Encode(e.canonicalRoot()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"id":"`+sid+`"}}`+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
	return path
}

// lockHooksInUserSettings writes the one user-scope setting that keeps
// every hook from loading, and returns what the reading of it names.
func (e *env) lockHooksInUserSettings() string {
	e.t.Helper()
	path := e.claude().UserSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"disableAllHooks": true}`), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return "disableAllHooks in " + string(claudesettings.SourceUser)
}

// readOnlyDir takes the write permission off a directory the machine is
// about to write into, and puts it back when the test ends so the
// temporary tree can be removed.
func (e *env) readOnlyDir(dir string) {
	e.t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

func (e *env) acceptCurrentAgreement() {
	e.t.Helper()
	if err := e.consentStore().AcceptAgreement(consent.AgreementVersion, "2026-08-01T00:00:00Z"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) enable(shape proxytest.Shape) {
	e.t.Helper()
	if err := e.machine().Enable(e.project, shape, e.io()); err != nil {
		e.t.Fatalf("enable: %v\nstdout: %s\nstderr: %s", err, e.stdout, e.stderr)
	}
}

// indexAll fails unless every marker appears in out, and returns their
// positions in the order given.
func indexAll(t *testing.T, out string, markers ...string) []int {
	t.Helper()
	positions := make([]int, len(markers))
	for i, m := range markers {
		positions[i] = strings.Index(out, m)
		if positions[i] < 0 {
			t.Fatalf("missing %q in output:\n%s", m, out)
		}
	}
	return positions
}

func assertOrdered(t *testing.T, out string, markers ...string) {
	t.Helper()
	positions := indexAll(t, out, markers...)
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Errorf("%q appears before %q in output:\n%s", markers[i], markers[i-1], out)
		}
	}
}

func TestEnable_StepsAppearInOrder(t *testing.T) {
	oldest := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		prepare func(e *env)
		stdin   string
		want    []string
		absent  []string
	}{
		{
			name: "every conditional step",
			prepare: func(e *env) {
				if err := e.consentStore().AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
				e.lockHooksInUserSettings()
				e.environ["ANTHROPIC_BASE_URL"] = "https://relay.example.com"
				e.sessionFile("s-old", oldest)
				e.sessionFile("s-new", oldest.Add(48*time.Hour))
			},
			stdin: "yes\n",
			want: []string{
				"The data agreement changed since you last accepted it.",
				consent.AgreementText,
				deviceWideTermsLine,
				agreementPrompt,
				hooksWillNotLoadLine,
				proxyHalfOnlyLine,
				"Detected an existing base URL",
				earlierSessionsLine(2, oldest),
				"Injected ",
				"Self-check passed",
				remoteControlLine,
				contributesLine,
			},
		},
		{
			name:    "nothing conditional",
			prepare: func(e *env) { e.acceptCurrentAgreement() },
			stdin:   "",
			want: []string{
				earlierSessionsLine(0, time.Time{}),
				"Injected ",
				"Self-check passed",
				remoteControlLine,
				contributesLine,
			},
			absent: []string{agreementPrompt, hooksWillNotLoadLine, "Detected an existing base URL", enableAnywayPrompt},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			tt.prepare(e)
			e.stdin = tt.stdin

			e.enable(proxytest.WithProxy)

			out := e.stdout.String()
			assertOrdered(t, out, tt.want...)
			for _, m := range tt.absent {
				if strings.Contains(out, m) {
					t.Errorf("unexpected %q in output:\n%s", m, out)
				}
			}
			if !e.status().Consistent() {
				t.Error("project is not consistently enabled afterwards")
			}
		})
	}
}

func TestEnable_PrintsEarlierSessionCountBeforeInjecting(t *testing.T) {
	oldest := time.Date(2025, 11, 3, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		prepare func(e *env)
		want    string
	}{
		{
			name:    "no earlier sessions",
			prepare: func(*env) {},
			want:    earlierSessionsLine(0, time.Time{}),
		},
		{
			name: "three earlier sessions with the oldest date",
			prepare: func(e *env) {
				e.sessionFile("s-1", oldest.Add(72*time.Hour))
				e.sessionFile("s-2", oldest)
				e.sessionFile("s-3", oldest.Add(24*time.Hour))
			},
			want: earlierSessionsLine(3, oldest),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			e.acceptCurrentAgreement()
			e.stdin = ""
			tt.prepare(e)

			e.enable(proxytest.WithProxy)

			out := e.stdout.String()
			assertOrdered(t, out, tt.want, "Injected ")
			after := out[strings.Index(out, tt.want):]
			for _, prompt := range []string{"[y/N]", "[yes/no]"} {
				if strings.Contains(after, prompt) {
					t.Errorf("a question follows the count:\n%s", after)
				}
			}
			if strings.Contains(out, truncatedLine()) {
				t.Errorf("count reported as incomplete for a small tree:\n%s", out)
			}
		})
	}
}

func TestEnable_SaysTheCountIsIncompleteAboveTheWalkLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("creates more directories than the walk visits")
	}
	e := newEnv(t)
	e.startProxy()
	e.acceptCurrentAgreement()
	e.stdin = ""
	e.sessionFile("s-1", time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))
	for i := 0; i < discover.Limit; i++ {
		if err := os.Mkdir(filepath.Join(e.project, fmt.Sprintf("d%05d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	e.enable(proxytest.WithProxy)

	assertOrdered(t, e.stdout.String(), " earlier session record(s) will be collected once", truncatedLine(), "Injected ")
}

func TestEnable_AcceptsTermsForTheDevice(t *testing.T) {
	e := newEnv(t)
	e.startProxy()

	e.enable(proxytest.WithProxy)

	assertOrdered(t, e.stdout.String(), consent.AgreementText, deviceWideTermsLine, agreementPrompt)
}

func hookCommands(t *testing.T, settingsPath string) map[string][]string {
	t.Helper()
	settings := readSettings(t, settingsPath)
	hooks, _ := settings["hooks"].(map[string]any)
	commands := map[string][]string{}
	for event, groups := range hooks {
		for _, group := range groups.([]any) {
			for _, entry := range group.(map[string]any)["hooks"].([]any) {
				commands[event] = append(commands[event], entry.(map[string]any)["command"].(string))
			}
		}
	}
	return commands
}

func TestEnable_NoProxyInjectsThreeHooksWithoutBaseURL(t *testing.T) {
	e := newEnv(t)
	e.startProxy()

	e.enable(proxytest.WithoutProxy)

	st := e.status()
	if !st.Enabled || st.Shape != proxytest.WithoutProxy {
		t.Errorf("status = %+v, want a grant recording the shape without a base URL", st)
	}
	if st.InjectedBaseURL != "" || !st.InjectionAgrees || !st.HookInstalled || !st.SessionEndInstalled {
		t.Errorf("status = %+v, want three hooks and no base URL", st)
	}
	if !st.Consistent() {
		t.Error("status does not read as consistent")
	}
	if grant, ok := e.sandbox.ActiveGrant(e.canonicalRoot()); !ok || grant.Shape != proxytest.WithoutProxy || grant.Token != st.Token {
		t.Errorf("grant = %+v, want the token granted in the shape without a base URL", grant)
	}
	settings := readSettings(t, e.settingsPath())
	if env, ok := settings["env"].(map[string]any); ok && env["ANTHROPIC_BASE_URL"] != nil {
		t.Errorf("a base URL was injected: %v", env["ANTHROPIC_BASE_URL"])
	}
	commands := hookCommands(t, e.settingsPath())
	for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
		if got := commands[event]; len(got) != 1 || got[0] != e.projectHooks().EnsureProxy+" --no-proxy" {
			t.Errorf("%s hooks = %q, want the ensure-proxy command marked --no-proxy", event, got)
		}
	}
	if got := commands["SessionEnd"]; len(got) != 1 || got[0] != e.projectHooks().SessionEnd {
		t.Errorf("SessionEnd hooks = %q, want the session-end command unmarked", got)
	}
	out := e.stdout.String()
	assertOrdered(t, out, "Injected ", "Self-check passed", noProxyFactLine, contributesLine)
	if strings.Contains(out, remoteControlLine) {
		t.Error("the Remote Control notice was said for the shape that keeps it available")
	}
}

func TestEnable_NoProxyWithHooksThatWillNotRunAsksFirst(t *testing.T) {
	tests := []struct {
		name        string
		stdin       string
		wantEnabled bool
	}{
		{"default answer leaves the project as it is", "\n", false},
		{"no leaves the project as it is", "no\n", false},
		{"yes enables anyway", "y\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			e.acceptCurrentAgreement()
			reason := e.lockHooksInUserSettings()
			e.sessionFile("s-1", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
			consentBefore := e.consentFileContents()
			e.stdin = tt.stdin

			e.enable(proxytest.WithoutProxy)

			out := e.stdout.String()
			assertOrdered(t, out, hooksWillNotLoadLine+" ("+reason+")", noProxyNothingLine, enableAnywayPrompt)
			if strings.Contains(out, proxyHalfOnlyLine) {
				t.Errorf("the consequence of the other shape was said:\n%s", out)
			}
			st := e.status()
			if st.Enabled != tt.wantEnabled {
				t.Fatalf("enabled = %v, want %v\n%s", st.Enabled, tt.wantEnabled, out)
			}
			if tt.wantEnabled {
				if st.Shape != proxytest.WithoutProxy || !st.Consistent() {
					t.Errorf("status = %+v, want the shape without a base URL installed", st)
				}
				return
			}
			if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
				t.Error("settings file written for a project left as it is")
			}
			if st.Injected || st.ConsentState != "" {
				t.Errorf("status = %+v, want nothing recorded", st)
			}
			if got := e.consentFileContents(); got != consentBefore {
				t.Errorf("consent file changed:\n%s", got)
			}
			if _, ok := e.sandbox.ActiveGrant(e.canonicalRoot()); ok {
				t.Error("a grant was written")
			}
			if paths := e.registeredPaths(e.canonicalRoot()); len(paths) != 0 {
				t.Errorf("session files registered: %v", paths)
			}
			if !strings.Contains(out, "Nothing was changed.") || strings.Contains(out, contributesLine) {
				t.Errorf("output does not say the project was left alone:\n%s", out)
			}
		})
	}
}

func TestEnable_WithProxyAndDeadHooksSaysSo(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.acceptCurrentAgreement()
	reason := e.lockHooksInUserSettings()
	e.stdin = ""

	e.enable(proxytest.WithProxy)

	out := e.stdout.String()
	assertOrdered(t, out, hooksWillNotLoadLine+" ("+reason+")", proxyHalfOnlyLine, "Injected ", contributesLine)
	for _, m := range []string{noProxyNothingLine, enableAnywayPrompt} {
		if strings.Contains(out, m) {
			t.Errorf("unexpected %q:\n%s", m, out)
		}
	}
	if !e.status().Consistent() {
		t.Error("project not enabled")
	}
}

func TestEnable_SaysRemoteControlOnce(t *testing.T) {
	tests := []struct {
		name          string
		shape         proxytest.Shape
		wantRC, wantF int
	}{
		{"with a base URL", proxytest.WithProxy, 1, 0},
		{"without a base URL", proxytest.WithoutProxy, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()

			e.enable(tt.shape)

			out := e.stdout.String()
			if got := strings.Count(out, remoteControlLine); got != tt.wantRC {
				t.Errorf("Remote Control notice appears %d times, want %d:\n%s", got, tt.wantRC, out)
			}
			if got := strings.Count(out, noProxyFactLine); got != tt.wantF {
				t.Errorf("shape sentence appears %d times, want %d:\n%s", got, tt.wantF, out)
			}
			if strings.Count(out, contributesLine) != 1 {
				t.Errorf("closing line count != 1:\n%s", out)
			}
		})
	}
}

func TestEnable_SwitchesShapeCleanly(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.enable(proxytest.WithProxy)
	first := e.status()
	withProxy, err := os.ReadFile(e.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	e.stdin = ""

	e.enable(proxytest.WithoutProxy)
	middle := e.status()
	if middle.Shape != proxytest.WithoutProxy || middle.InjectedBaseURL != "" || !middle.Consistent() {
		t.Fatalf("status after switching to --no-proxy = %+v", middle)
	}
	if middle.Token != first.Token {
		t.Errorf("token changed on a shape switch: %q -> %q", first.Token, middle.Token)
	}
	if !strings.Contains(e.stdout.String(), noProxyFactLine) {
		t.Errorf("shape sentence missing:\n%s", e.stdout)
	}

	e.enable(proxytest.WithProxy)
	last := e.status()
	if last.Shape != proxytest.WithProxy || last.InjectedToken != first.Token || !last.Consistent() {
		t.Fatalf("status after switching back = %+v", last)
	}
	back, err := os.ReadFile(e.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(withProxy) {
		t.Errorf("settings after switching back differ from the first enable:\n%s\nwant:\n%s", back, withProxy)
	}
}

func TestEnable_SwitchingShapeKeepsAUsersOwnBaseURL(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	e.stdin = ""

	e.enable(proxytest.WithoutProxy)

	if got := ownBaseURL(t, e); got != relayInSettingsLocal {
		t.Errorf("after switching to --no-proxy the project's own base URL is %q, want %q back", got, relayInSettingsLocal)
	}
	if st := e.status(); st.Shape != proxytest.WithoutProxy || !st.Consistent() {
		t.Errorf("status = %+v", st)
	}
}

func TestEnable_RegistersExistingSessionFiles(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	at := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	main := e.sessionFile("s-1", at)
	agentDir := filepath.Join(filepath.Dir(main), "s-1", "subagents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(agentDir, "agent-a.jsonl")
	if err := os.WriteFile(agent, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	e.enable(proxytest.WithProxy)

	if got := e.registeredPaths(e.canonicalRoot()); len(got) != 2 || got[0] != main && got[1] != main || got[0] != agent && got[1] != agent {
		t.Errorf("registered = %v, want %q and %q", got, main, agent)
	}
	if !strings.Contains(e.stdout.String(), earlierSessionsLine(1, at)) {
		t.Errorf("count missing:\n%s", e.stdout)
	}
}

func TestEnable_RollbackUnregistersWhatItRegistered(t *testing.T) {
	at := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		registered bool
	}{
		{"a registry the install created is taken back", false},
		{"a registry that stood before is left alone", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.occupyPort()
			found := e.sessionFile("s-found", at)
			earlier := filepath.Join(filepath.Dir(found), "s-earlier.jsonl")
			if tt.registered {
				e.sandbox.RegisterSessionFile(e.status().Hash, earlier, "")
			}

			if err := e.machine().Enable(e.project, proxytest.WithProxy, e.io()); err == nil {
				t.Fatal("enable succeeded against a foreign port")
			}

			got := e.registeredPaths(e.canonicalRoot())
			switch {
			case !tt.registered && len(got) != 0:
				t.Errorf("registry survived rollback: %v", got)
			case tt.registered && (len(got) == 0 || got[0] != earlier):
				t.Errorf("registry after rollback = %v, want %q kept", got, earlier)
			}
		})
	}
}

func TestEnable_RollsBackEveryChangeWhereverItFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	const ignoreBefore = "build/\n"
	tests := []struct {
		name    string
		prepare func(e *env)
		wantErr string
	}{
		{
			name: "the settings file cannot be written",
			prepare: func(e *env) {
				e.readOnlyDir(filepath.Dir(e.settingsPath()))
			},
			wantErr: "injecting ",
		},
		{
			name: "the ignore lines cannot be written",
			prepare: func(e *env) {
				e.readOnlyDir(e.canonicalRoot())
			},
			wantErr: "ensuring .gitignore covers",
		},
		{
			name:    "the self-check finds a foreign port",
			prepare: func(e *env) { e.occupyPort() },
			wantErr: "self-check failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.gitRepo()
			e.sandbox.Pause(proxytest.PauseConsentReconfirm)
			e.sessionFile("s-1", time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC))
			ignorePath := filepath.Join(e.canonicalRoot(), ".gitignore")
			if err := os.WriteFile(ignorePath, []byte(ignoreBefore), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(e.settingsPath()), 0o755); err != nil {
				t.Fatal(err)
			}
			tt.prepare(e)

			err := e.machine().Enable(e.project, proxytest.WithProxy, e.io())

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "all changes rolled back") {
				t.Fatalf("err = %v, want %q and every change taken back", err, tt.wantErr)
			}
			if st := e.status(); st.Enabled || st.Injected || st.ConsentState != "" {
				t.Errorf("status = %+v, want nothing recorded for the project", st)
			}
			if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
				t.Error("the settings injection survived the rollback")
			}
			if got := e.registeredPaths(e.canonicalRoot()); len(got) != 0 {
				t.Errorf("registered session files survived the rollback: %v", got)
			}
			if got, err := os.ReadFile(ignorePath); err != nil || string(got) != ignoreBefore {
				t.Errorf(".gitignore = %q, %v, want %q", got, err, ignoreBefore)
			}
			version, _, err := e.consentStore().AcceptedVersion()
			if err != nil || version != consent.AgreementVersion {
				t.Errorf("accepted agreement version = %q, %v, want %q kept", version, err, consent.AgreementVersion)
			}
			if reason := e.sandbox.PausedReason(); reason != "" {
				t.Errorf("capture is paused for %q after the rollback", reason)
			}
		})
	}
}

func TestDisable_UnregistersTheProject(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.sessionFile("s-1", time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC))
	e.enable(proxytest.WithProxy)
	if len(e.registeredPaths(e.canonicalRoot())) != 1 {
		t.Fatal("precondition: enable registers the session file")
	}

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if got := e.registeredPaths(e.canonicalRoot()); len(got) != 0 {
		t.Errorf("registry survived disable: %v", got)
	}
	for _, hash := range e.sandbox.ProjectsWithRegistry() {
		if hash == e.status().Hash {
			t.Error("the project's registry file still exists")
		}
	}
}

func TestDisableAndUninstall_LeaveSessionFilesUntouched(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	at := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	paths := []string{e.sessionFile("s-1", at), e.sessionFile("s-2", at.Add(time.Hour))}
	before := map[string][]byte{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = data
	}
	check := func(step string) {
		t.Helper()
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("after %s: %v", step, err)
			}
			if string(data) != string(before[p]) {
				t.Errorf("after %s %s changed:\n%s", step, p, data)
			}
		}
	}

	e.enable(proxytest.WithProxy)
	check("enable")
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	check("disable")
	e.stdin = ""
	if err := e.machine().Uninstall(true, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	check("uninstall --delete-data")
}

func TestDoctorRewritesTheInjectionInTheGrantsShape(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.enable(proxytest.WithoutProxy)
	// The file was edited into the other shape behind the grant's back.
	if err := claudesettings.RemoveProject(e.settingsPath()); err != nil {
		t.Fatal(err)
	}
	if err := claudesettings.InjectProject(e.settingsPath(), "http://127.0.0.1:1/t/"+e.status().Token, e.projectHooks()); err != nil {
		t.Fatal(err)
	}
	if st := e.status(); st.Consistent() || st.Shape != proxytest.WithoutProxy || st.InjectedBaseURL == "" {
		t.Fatalf("precondition: status = %+v", st)
	}
	e.stdout.Reset()

	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatalf("doctor: %v", err)
	}

	if !strings.Contains(e.stdout.String(), "rewrote the injection") {
		t.Errorf("doctor = %q, want the rewrite reported", e.stdout)
	}
	if st := e.status(); st.Shape != proxytest.WithoutProxy || st.InjectedBaseURL != "" || !st.Consistent() {
		t.Errorf("status after doctor = %+v, want the grant's shape restored", st)
	}
}

func TestDoctorReadsOneShapeWhenTheSettingsFileDisagreesWithTheGrant(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-proj",
		ProjectIDHash: e.status().Hash,
		RootPath:      e.canonicalRoot(),
		Upstream:      "https://api.anthropic.com",
		Shape:         proxytest.WithProxy,
	})
	// The file was edited into the other shape behind the grant's back.
	e.injectWithoutBaseURL()

	problems, out := e.doctor()

	if strings.Contains(out, "runs only while a session is open") {
		t.Errorf("doctor = %q, want the proxy finding to read the shape the injection is repaired to", out)
	}
	if !strings.Contains(out, "rewrote the injection") {
		t.Errorf("doctor = %q, want the injection rewritten in the shape the grant records", out)
	}
	if st := e.status(); st.InjectedBaseURL == "" {
		t.Errorf("status after doctor = %+v, want the base URL of the grant's shape injected", st)
	}
	if problems == 0 {
		t.Errorf("problems = %d, doctor = %q; want the proxy this project needs reported as down", problems, out)
	}
}

func TestGrantRecordsTheShapeAcrossReads(t *testing.T) {
	e := newEnv(t)
	e.sandbox.GrantProject(proxytest.Grant{
		Token: "tok-shape", ProjectIDHash: e.status().Hash, RootPath: e.canonicalRoot(),
		Upstream: "https://api.anthropic.com", Shape: proxytest.WithoutProxy,
	})
	data, err := os.ReadFile(e.layout().RoutingTable())
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if table.Projects["tok-shape"]["no_proxy"] != true {
		t.Errorf("table = %s, want no_proxy recorded", data)
	}
	if e.status().Shape != proxytest.WithoutProxy {
		t.Error("the shape did not read back from the grant")
	}
}

func TestEnable_RollsBackWhenSessionFilesCannotBeRegistered(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.sessionFile("s-1", time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC))
	e.obstruct(e.layout().FollowDir())

	err := e.machine().Enable(e.project, proxytest.WithProxy, e.io())
	if err == nil || !strings.Contains(err.Error(), "session files") || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v, want the registration failure with the rollback notice", err)
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("settings injection survived rollback")
	}
	if e.status().Enabled {
		t.Error("routing grant survived rollback")
	}
}

func TestEnable_FailsWhenTheProjectTreeCannotBeListed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	e := newEnv(t)
	e.startProxy()
	e.acceptCurrentAgreement()
	e.stdin = ""
	root := e.canonicalRoot()
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) })

	err := e.machine().Enable(e.project, proxytest.WithProxy, e.io())
	if err == nil || !strings.Contains(err.Error(), "looking for this project's session files") {
		t.Fatalf("err = %v, want the walk failure named", err)
	}
	if e.status().Enabled {
		t.Error("project granted although its tree could not be listed")
	}
}

func TestEnable_SwitchingShapeInsideASessionNamesTheBaseURLItCouldNotPutBack(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	e.environ["ANTHROPIC_BASE_URL"] = e.status().InjectedBaseURL
	e.stdin = ""

	e.enable(proxytest.WithoutProxy)

	if !strings.Contains(e.stderr.String(), relayInSettingsLocal) {
		t.Errorf("switching to --no-proxy dropped the user's own base URL %q without naming it:\nstderr: %s", relayInSettingsLocal, e.stderr)
	}
	if st := e.status(); st.Shape != proxytest.WithoutProxy || !st.Consistent() {
		t.Errorf("status = %+v", st)
	}
}

func TestEnable_NoProxyFailsWhenTheAnswerIsUnavailable(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.acceptCurrentAgreement()
	e.lockHooksInUserSettings()
	e.stdin = ""

	err := e.machine().Enable(e.project, proxytest.WithoutProxy, e.io())
	if err == nil || !strings.Contains(err.Error(), "reading the answer") {
		t.Fatalf("err = %v, want the unavailable answer reported", err)
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("settings file written without an answer")
	}
	if e.status().Enabled {
		t.Error("project granted without an answer")
	}
}

func TestDisable_FailsLoudlyWhenTheRegistryCannotBeRemoved(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.sessionFile("s-1", time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC))
	e.enable(proxytest.WithProxy)
	e.obstruct(e.layout().FollowDir())

	err := e.machine().Disable(e.project, false, e.io())
	if err == nil || !strings.Contains(err.Error(), "withdrawing this project's session files") {
		t.Fatalf("err = %v, want the registry failure named", err)
	}
	if st := e.status(); st.Enabled || st.Injected {
		t.Fatalf("status = %+v, want traffic stopped before the failing step", st)
	}
	// The rerun takes the withdrawal-in-progress path and meets the same
	// obstacle.
	err = e.machine().Disable(e.project, false, e.io())
	if err == nil || !strings.Contains(err.Error(), "withdrawing this project's session files") {
		t.Fatalf("rerun err = %v, want the registry failure named again", err)
	}
}

func TestDoctorPutsBackAUsersOwnBaseURLWhenTheGrantsShapeHasNoBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		masked   bool
		wantLine string
	}{
		{"chain quiet: the relay goes back into the file", false, "Put back the base URL of your own that the injection had displaced: " + relayInSettingsLocal},
		{"chain masked: the relay is named, not written", true, "could not put back a base URL of your own"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := enabledOverAUsersOwnRelay(t)
			st := e.status()
			// The grant was switched to the shape without a base URL behind
			// the settings file's back.
			e.sandbox.GrantProject(proxytest.Grant{
				Token: st.Token, ProjectIDHash: st.Hash, RootPath: st.Root,
				Upstream: relayInSettingsLocal, Shape: proxytest.WithoutProxy,
			})
			if tt.masked {
				e.environ["ANTHROPIC_BASE_URL"] = st.InjectedBaseURL
			}
			e.stdout.Reset()

			if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
				t.Fatalf("doctor: %v", err)
			}

			out := e.stdout.String()
			if !strings.Contains(out, "rewrote the injection") || !strings.Contains(out, tt.wantLine) {
				t.Errorf("doctor = %q, want the rewrite and %q", out, tt.wantLine)
			}
			after := e.status()
			if after.Shape != proxytest.WithoutProxy || after.InjectedBaseURL != "" || !after.Consistent() {
				t.Errorf("status after doctor = %+v, want the grant's shape", after)
			}
			if got := ownBaseURL(t, e); !tt.masked && got != relayInSettingsLocal {
				t.Errorf("own base URL after doctor = %q, want %q", got, relayInSettingsLocal)
			}
		})
	}
}
