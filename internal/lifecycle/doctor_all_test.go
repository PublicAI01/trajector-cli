package lifecycle_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/report"
)

// doctorAll runs `doctor --all` from dir and returns its count and
// report.
func (e *env) doctorAll(dir string) (int, string) {
	e.t.Helper()
	e.stdout.Reset()
	problems, err := e.machine().DoctorAll(e.t.Context(), dir, e.io())
	if err != nil {
		e.t.Fatalf("doctor --all: %v\nstdout: %s", err, e.stdout)
	}
	return problems, e.stdout.String()
}

// enableAt enables the project at dir through the proxy, as `trajector
// enable` run there would, and returns its root as the routing table
// records it.
func (e *env) enableAt(dir string) string {
	e.t.Helper()
	if err := e.machine().Enable(e.t.Context(), dir, choices(proxytest.WithProxy), e.io()); err != nil {
		e.t.Fatalf("enable %s: %v\nstdout: %s", dir, err, e.stdout)
	}
	return proxytest.CanonicalRoot(e.t, dir)
}

// sectionsOf splits a doctor --all report at its headings: every line
// that is not indented and is neither the first line nor the verdict.
// It returns the headings in the order printed and the lines under
// each.
func sectionsOf(out string) (headings []string, body map[string]string) {
	body = map[string]string{}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	current := ""
	for _, line := range lines[1 : len(lines)-1] {
		switch {
		case line == "":
		case strings.HasPrefix(line, " "):
			body[current] += line + "\n"
		default:
			current = line
			headings = append(headings, line)
		}
	}
	return headings, body
}

func TestDoctorAllChecksEachEnabledProjectUnderItsRoot(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	a := e.enableAt(t.TempDir())
	b := e.enableAt(t.TempDir())
	// The first pass repairs what enable does not own (the discovery
	// hint); the run under test has nothing left to repair.
	e.doctorAll(t.TempDir())

	problems, out := e.doctorAll(t.TempDir())
	if problems != 0 {
		t.Fatalf("problems = %d on two healthy projects, output:\n%s", problems, out)
	}
	headings, body := sectionsOf(out)
	want := []string{"Device", a, b}
	slices.Sort(want[1:])
	if !slices.Equal(headings, want) {
		t.Fatalf("headings = %q, want the device and then each root once, sorted (%q)\n%s", headings, want, out)
	}
	for _, root := range []string{a, b} {
		for _, line := range []string{
			"injection and routing agree for this project",
			"every session file of this project is registered",
			"live proxy confirms this project routes and records",
		} {
			if !strings.Contains(body[root], line) {
				t.Errorf("section %s lacks %q:\n%s", root, line, body[root])
			}
		}
	}
	// The device's part is said once, ahead of every project.
	for _, line := range []string{"automatic upgrades:", "proxy running at", "capture spool writable", "no rejected batches quarantined"} {
		if n := strings.Count(out, line); n != 1 {
			t.Errorf("%q appears %d times, want once:\n%s", line, n, out)
		}
		if !strings.Contains(body["Device"], line) {
			t.Errorf("%q is not in the device section:\n%s", line, body["Device"])
		}
	}
	if !strings.HasSuffix(out, "\nEverything checks out.\n") {
		t.Errorf("doctor --all = %q, want a clean verdict", out)
	}
}

func TestDoctorAllCountsEveryProjectsProblems(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	a := e.enableAt(t.TempDir())
	b := e.enableAt(t.TempDir())
	for _, root := range []string{a, b} {
		grant, ok := e.sandbox.ActiveGrant(root)
		if !ok {
			t.Fatalf("no grant for %s", root)
		}
		grant.Upstream = "not a url at all"
		e.sandbox.GrantProject(grant)
	}

	problems, out := e.doctorAll(t.TempDir())
	if problems != 2 {
		t.Fatalf("problems = %d, want one for each project, output:\n%s", problems, out)
	}
	_, body := sectionsOf(out)
	for _, root := range []string{a, b} {
		if !strings.Contains(body[root], "live proxy will not record") {
			t.Errorf("section %s lacks its own problem:\n%s", root, body[root])
		}
	}
	if !strings.HasSuffix(out, "\n2 problem(s) need attention.\n") {
		t.Errorf("doctor --all = %q, want the sum in the verdict", out)
	}
}

func TestDoctorAllSkipsARootThatIsGone(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	kept := e.enableAt(t.TempDir())
	goneDir := t.TempDir()
	gone := e.enableAt(goneDir)
	if err := os.RemoveAll(goneDir); err != nil {
		t.Fatal(err)
	}
	e.doctorAll(t.TempDir())

	problems, out := e.doctorAll(t.TempDir())
	if problems != 0 {
		t.Fatalf("problems = %d, want a gone root not counted, output:\n%s", problems, out)
	}
	headings, body := sectionsOf(out)
	if !slices.Contains(headings, gone) || !slices.Contains(headings, kept) {
		t.Fatalf("headings = %q, want both roots\n%s", headings, out)
	}
	if got := strings.TrimSpace(body[gone]); got != "note: this directory is not there any more, so it was skipped" {
		t.Errorf("section %s = %q, want the one skip line", gone, got)
	}
	if !strings.Contains(body[kept], "live proxy confirms") {
		t.Errorf("section %s = %q, want the project checked", kept, body[kept])
	}
}

func TestDoctorAllHeadsEachRootOnce(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	dir := t.TempDir()
	root := e.enableAt(dir)
	// Enabling again revokes the first grant and records a second.
	e.enableAt(dir)
	// A table edited by hand can hold two standing grants for one root.
	doubleGrant(t, e.sandbox.RoutingTablePath(), root)
	revoked := e.enableAt(t.TempDir())
	e.sandbox.RevokeProject(revoked, "")

	_, out := e.doctorAll(t.TempDir())
	headings, _ := sectionsOf(out)
	if want := []string{"Device", root}; !slices.Equal(headings, want) {
		t.Errorf("headings = %q, want %q: one section a root, none for a revoked grant\n%s", headings, want, out)
	}
}

// doubleGrant copies root's standing grant under a second token, as a
// table edited by hand could hold it.
func doubleGrant(t *testing.T, path, root string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var table map[string]any
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	projects, _ := table["projects"].(map[string]any)
	var standing any
	for _, rec := range projects {
		if r, _ := rec.(map[string]any); r["root_path"] == root && r["revoked_at"] == nil {
			standing = r
		}
	}
	if standing == nil {
		t.Fatalf("no standing grant for %s in %s", root, data)
	}
	projects["tok-copied-by-hand"] = standing
	if data, err = json.Marshal(table); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorAllStatesADevicePauseOnceForEveryProject(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	a := e.enableAt(t.TempDir())
	b := e.enableAt(t.TempDir())
	e.doctorAll(t.TempDir())
	e.sandbox.Pause(proxytest.PauseSignedOut)

	problems, out := e.doctorAll(t.TempDir())
	if problems != 1 {
		t.Errorf("problems = %d, want the pause counted once, output:\n%s", problems, out)
	}
	if n := strings.Count(out, proxytest.PauseSignedOut.Why()); n != 1 {
		t.Errorf("why the pause stands appears %d times, want once:\n%s", n, out)
	}
	_, body := sectionsOf(out)
	for _, root := range []string{a, b} {
		if strings.TrimSpace(lastLine(body[root])) != selfcheckUnderPause {
			t.Errorf("section %s ends with %q, want the self-check to point at the pause above:\n%s", root, lastLine(body[root]), body[root])
		}
	}
}

// lastLine is the last line of a section body.
func lastLine(body string) string {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestDoctorAllExplainsInFullAPauseTheDevicePartDidNotReport(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.enableAt(t.TempDir())
	e.enableAt(t.TempDir())
	e.doctorAll(t.TempDir())
	// The pause is written while the first project's part runs, after
	// the device part is printed: the device part reports no pause, and
	// the second project's part reads the pause from the table as well
	// as from the proxy.
	getenv := e.deps.Getenv
	e.deps.Getenv = func(key string) string {
		if strings.Contains(e.stdout.String(), report.DoctorDeviceHeading) && e.sandbox.PausedReason() == "" {
			e.sandbox.Pause(proxytest.PauseSignedOut)
		}
		return getenv(key)
	}

	problems, out := e.doctorAll(t.TempDir())
	if e.sandbox.PausedReason() == "" {
		t.Fatalf("the pause was never written, output:\n%s", out)
	}
	headings, body := sectionsOf(out)
	if strings.Contains(body[report.DoctorDeviceHeading], "paused everywhere") {
		t.Fatalf("the device part reported the pause, so it was written too early:\n%s", out)
	}
	for _, h := range headings {
		if strings.Contains(body[h], "(see above)") {
			t.Errorf("section %s points at a pause the device part did not report:\n%s", h, out)
		}
	}
	if problems != 2 {
		t.Errorf("problems = %d, want each project's self-check counted, output:\n%s", problems, out)
	}
}
