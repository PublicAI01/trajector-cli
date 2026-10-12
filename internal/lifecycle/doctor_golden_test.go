package lifecycle_test

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

var updateGolden = flag.Bool("update", false, "rewrite the doctor golden files")

// uptime is the one part of a doctor report that changes from run to
// run on the same device: how long the proxy has been up.
var uptime = regexp.MustCompile(`, up [^)]*\)`)

// pinDoctor holds out against the golden file name: the report with
// every path this test's device chose for itself replaced by a name.
func (e *env) pinDoctor(name, out string) {
	e.t.Helper()
	for _, sub := range []struct{ from, to string }{
		{e.canonicalRoot(), "<project>"},
		{e.project, "<project>"},
		{e.deps.Home, "<home>"},
		{e.deps.ProxyAddr, "<proxy>"},
	} {
		out = strings.ReplaceAll(out, sub.from, sub.to)
	}
	for i, root := range e.deps.Layout.Roots() {
		out = strings.ReplaceAll(out, root, "<layout"+string(rune('0'+i))+">")
	}
	out = uptime.ReplaceAllString(out, ", up <uptime>)")
	golden := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		e.t.Fatal(err)
	}
	if out != string(want) {
		e.t.Errorf("doctor diverged from %s\ngot:\n%s\nwant:\n%s", golden, out, want)
	}
}

// The plain doctor run is pinned byte for byte: the run that covers
// every project is built from the same checks, and splitting them must
// not move a line of the run that covers one.
func TestDoctorOutputIsPinned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("doctor adds a line of its own on Windows; the pin is the unix report")
	}
	t.Run("not enabled", func(t *testing.T) {
		e := newEnv(t)
		_, out := e.doctor()
		e.pinDoctor("doctor-not-enabled", out)
	})
	t.Run("first pass after enable", func(t *testing.T) {
		e := newEnv(t)
		e.startProxy()
		e.enable(proxytest.WithProxy)
		e.stdout.Reset()
		_, out := e.doctor()
		e.pinDoctor("doctor-first-pass", out)
	})
	t.Run("repairs and problems", func(t *testing.T) {
		e := newEnv(t)
		e.startProxy()
		e.enable(proxytest.WithProxy)
		e.dropSessionEndHook()
		grant, ok := e.sandbox.ActiveGrant(e.canonicalRoot())
		if !ok {
			t.Fatal("no grant after enable")
		}
		grant.Upstream = "not a url at all"
		e.sandbox.GrantProject(grant)
		e.stdout.Reset()
		_, out := e.doctor()
		e.pinDoctor("doctor-repairs-and-problems", out)
	})
}
