package lifecycle_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// enabledElsewhere is a project of this device other than e.project,
// granted in shape and injected the way enable would have injected it.
func (e *env) enabledElsewhere(tok string, shape proxytest.Shape) (root string, settings *proxytest.Settings) {
	e.t.Helper()
	root = proxytest.CanonicalRoot(e.t, e.t.TempDir())
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         tok,
		ProjectIDHash: "hash-" + tok,
		RootPath:      root,
		Upstream:      "https://api.anthropic.com",
		Shape:         shape,
	})
	baseURL := ""
	if shape == proxytest.WithProxy {
		baseURL = "http://127.0.0.1:41100/t/" + tok
	}
	settings = proxytest.ProjectSettings(e.t, root)
	settings.Inject(e.deps.ExecPath, baseURL)
	return root, settings
}

// withoutEvent rewrites a settings file as an injection made before
// this release ran a hook under event would have left it, and adds a
// key of the user's own beside the injection.
func withoutEvent(t *testing.T, settings *proxytest.Settings, event string) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(settings.Contents()), &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	if _, ok := hooks[event]; !ok {
		t.Fatalf("no hook under %s in %s", event, settings.Contents())
	}
	delete(hooks, event)
	root["permissions"] = map[string]any{"allow": []any{"Bash(go test:*)"}}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	settings.Put(string(data))
}

// keysBesideHooks is a settings file as its top-level keys other than
// hooks, each value compacted.
func keysBesideHooks(t *testing.T, settings *proxytest.Settings) map[string]string {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settings.Contents()), &raw); err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for key, value := range raw {
		if key == "hooks" {
			continue
		}
		var b bytes.Buffer
		if err := json.Compact(&b, value); err != nil {
			t.Fatal(err)
		}
		keys[key] = b.String()
	}
	return keys
}

// hookEvents is which events of a settings file carry a hook of the
// project injection, by marker.
func hookEvents(t *testing.T, settings *proxytest.Settings, marker string) []string {
	t.Helper()
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	if err := json.Unmarshal([]byte(settings.Contents()), &root); err != nil {
		t.Fatal(err)
	}
	var events []string
	for event, groups := range root.Hooks {
		for _, g := range groups {
			if slices.ContainsFunc(g.Hooks, func(h struct{ Command string }) bool { return strings.Contains(h.Command, marker) }) {
				events = append(events, event)
			}
		}
	}
	slices.Sort(events)
	return slices.Compact(events)
}

const completedTwo = "The session hooks of 2 enabled project(s) were completed for this release; `trajector doctor --all` lists them."

func TestFinishUpgradeCompletesTheHooksOfEnabledProjectsAndNothingElse(t *testing.T) {
	e := newEnv(t)

	// Two projects an older release enabled, each short of a hook this
	// release runs under one more event, in either shape.
	_, short := e.enabledElsewhere("tok-short", proxytest.WithProxy)
	withoutEvent(t, short, "SubagentStop")
	_, shortNoProxy := e.enabledElsewhere("tok-short-np", proxytest.WithoutProxy)
	withoutEvent(t, shortNoProxy, "SessionEnd")
	besideShort, besideShortNoProxy := keysBesideHooks(t, short), keysBesideHooks(t, shortNoProxy)

	// Projects that are not to be touched: one already complete; one
	// granted whose injection is gone, which is the user's to restore;
	// one short of a hook whose grant was revoked; one whose directory
	// is gone.
	_, complete := e.enabledElsewhere("tok-complete", proxytest.WithProxy)
	_, uninjected := e.enabledElsewhere("tok-uninjected", proxytest.WithProxy)
	uninjected.RemoveInjection()
	revokedRoot, revoked := e.enabledElsewhere("tok-revoked", proxytest.WithProxy)
	withoutEvent(t, revoked, "SubagentStop")
	e.sandbox.RevokeProject(revokedRoot, "")
	goneRoot, _ := e.enabledElsewhere("tok-gone", proxytest.WithProxy)
	if err := os.RemoveAll(goneRoot); err != nil {
		t.Fatal(err)
	}
	untouched := map[*proxytest.Settings]string{}
	for _, s := range []*proxytest.Settings{complete, uninjected, revoked} {
		untouched[s] = s.Contents()
	}

	if err := e.machine().FinishUpgrade(t.Context()); err != nil {
		t.Fatalf("FinishUpgrade: %v", err)
	}

	if got, want := e.notices(), []string{upgradedTestv, completedTwo}; !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
	if got := hookEvents(t, short, proxytest.ProgressMarker); !slices.Equal(got, []string{"PostToolBatch", "Stop", "SubagentStop"}) {
		t.Errorf("progress hook after finishing stands under %v, want every event of it", got)
	}
	if got := keysBesideHooks(t, short); !maps.Equal(got, besideShort) {
		t.Errorf("keys beside hooks = %v, want them as they were: %v", got, besideShort)
	}
	if shape, ok := shortNoProxy.Shape(); !ok || shape != proxytest.WithoutProxy || !shortNoProxy.HasHook(proxytest.SessionEndMarker) {
		t.Errorf("project without a base URL after finishing: shape %v, %v, session-end hook %v; want the shape kept and the hook added",
			shape, ok, shortNoProxy.HasHook(proxytest.SessionEndMarker))
	}
	if got := keysBesideHooks(t, shortNoProxy); !maps.Equal(got, besideShortNoProxy) {
		t.Errorf("keys beside hooks = %v, want them as they were: %v", got, besideShortNoProxy)
	}
	for s, before := range untouched {
		if got := s.Contents(); got != before {
			t.Errorf("%s = %s, want it untouched:\n%s", s.Path(), got, before)
		}
	}
	if _, err := os.Stat(goneRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(%s) = %v, want the project that is gone left gone", goneRoot, err)
	}
	for _, s := range []*proxytest.Settings{short, shortNoProxy} {
		ignore := filepath.Join(filepath.Dir(filepath.Dir(s.Path())), ".gitignore")
		if _, err := os.Stat(ignore); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat(%s) = %v, want no .gitignore written", ignore, err)
		}
	}

	// Run again, every project is complete and it says only that it is
	// installed.
	if err := e.machine().FinishUpgrade(t.Context()); err != nil {
		t.Fatalf("FinishUpgrade again: %v", err)
	}
	if got := e.notices(); !slices.Equal(got, []string{upgradedTestv}) {
		t.Errorf("notices after finishing again = %q, want only %q", got, upgradedTestv)
	}
}

func TestFinishUpgradeThatCannotCompleteAProjectFailsAfterLeavingItsLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symbolic link needs a privilege Windows test runners lack")
	}
	e := newEnv(t)
	_, short := e.enabledElsewhere("tok-short", proxytest.WithProxy)
	withoutEvent(t, short, "SubagentStop")
	_, linked := e.enabledElsewhere("tok-linked", proxytest.WithProxy)
	withoutEvent(t, linked, "SubagentStop")
	target := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Rename(linked.Path(), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linked.Path()); err != nil {
		t.Fatal(err)
	}

	err := e.machine().FinishUpgrade(t.Context())
	if err == nil || !strings.Contains(err.Error(), linked.Path()) {
		t.Fatalf("FinishUpgrade = %v, want the file it could not complete named", err)
	}
	want := []string{upgradedTestv, strings.Replace(completedTwo, "2", "1", 1)}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
}

// TestFinishUpgradeNeverReachesAWholeInjection pins the repair's
// boundary in the code itself: nothing FinishUpgrade can reach in this
// package rewrites or removes an injection, sets a base URL, writes
// .gitignore or an optional setting, or touches the user-level hooks.
// Calls are matched by name across the package, which over-counts what
// is reachable and so can only fail where it should not, never pass
// where it should fail.
func TestFinishUpgradeNeverReachesAWholeInjection(t *testing.T) {
	fset := token.NewFileSet()
	bodies := map[string][]*ast.BlockStmt{}
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[fn.Name.Name] = append(bodies[fn.Name.Name], fn.Body)
			}
		}
	}
	forbidden := []string{
		"InjectProject", "injectProject", "removeInjection", "RemoveProject",
		"SetBaseURL",
		"EnsureGitIgnored", "RemoveGitIgnored",
		"SetTopLevelBool", "RestoreTopLevelBool", "offerOptionalSettings", "restoreRecordedSettings",
		"InjectUserHook", "RemoveUserHook",
	}
	reached := map[string]bool{"FinishUpgrade": true}
	queue := []string{"FinishUpgrade"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, body := range bodies[name] {
			ast.Inspect(body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var called string
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					called = fun.Name
				case *ast.SelectorExpr:
					called = fun.Sel.Name
				}
				if slices.Contains(forbidden, called) {
					t.Errorf("%s, which FinishUpgrade reaches, calls %s at %s", name, called, fset.Position(call.Pos()))
				}
				if _, ours := bodies[called]; ours && !reached[called] {
					reached[called] = true
					queue = append(queue, called)
				}
				return true
			})
		}
	}
	if !reached["completeEnabledProjects"] {
		t.Error("FinishUpgrade does not reach completeEnabledProjects")
	}
}

// The injection is the grant's and lacks only an event this release
// adds: doctor completes it as an upgrade would, so the base URL stays
// spelled as the file spells it and no key beside the hooks changes.
// A whole rewrite would have spelled it the way this proxy serves it.
func TestDoctorCompletesAnInjectionShortOfAnEventWithoutRewritingIt(t *testing.T) {
	for _, short := range []struct{ event, marker string }{
		{"SubagentStop", proxytest.ProgressMarker},
		{"UserPromptSubmit", proxytest.EnsureProxyMarker},
	} {
		t.Run(short.event, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			if err := e.machine().Enable(t.Context(), e.project, choices(proxytest.WithProxy), e.io()); err != nil {
				t.Fatal(err)
			}
			settings := e.projectSettings()
			served, ok := settings.InjectedBaseURL()
			if !ok {
				t.Fatal("enable injected no base URL")
			}
			tok, _ := proxytest.TokenFromBaseURL(served)
			spelled := "http://localhost:" + served[strings.LastIndex(served, ":")+1:]
			settings.Put(strings.Replace(settings.Contents(), served, spelled, 1))
			if got, _ := settings.InjectedBaseURL(); got != spelled || !strings.HasSuffix(spelled, "/t/"+tok) {
				t.Fatalf("base URL = %q, want %q", got, spelled)
			}
			withoutEvent(t, settings, short.event)
			before := keysBesideHooks(t, settings)
			if st := e.status(); !st.MissingSessionHooks() || !st.InjectionAgrees {
				t.Fatalf("status = %+v, want an agreeing injection short of a hook", st)
			}

			e.stdout.Reset()
			problems, out := e.doctor()
			if problems != 0 || !strings.Contains(out, "completed the session hooks in ") || strings.Contains(out, "rewrote the injection") {
				t.Fatalf("doctor = %d problems, output:\n%s\nwant the hooks completed, not the injection rewritten", problems, out)
			}
			if got := keysBesideHooks(t, settings); !maps.Equal(got, before) {
				t.Errorf("keys beside hooks = %v, want them as they were: %v", got, before)
			}
			if got := hookEvents(t, settings, short.marker); !slices.Contains(got, short.event) {
				t.Errorf("hook events of %s after doctor = %v, want %s among them", short.marker, got, short.event)
			}
			if st := e.status(); !st.Hooks.Complete() || !st.Consistent() {
				t.Errorf("status after doctor = %+v, want every hook in place", st)
			}
		})
	}
}
