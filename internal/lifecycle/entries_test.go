package lifecycle_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestDoctorCountsAnEntryALaterBuildNeedsAsRegistered(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	e.enable(proxytest.WithProxy)
	path := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(-48*time.Hour))
	e.registerFile(e.canonicalRoot(), path, "")
	e.sandbox.NeedALaterBuild(e.status().Hash, path, "a-later-build")
	if got := e.registeredFiles(e.canonicalRoot()); len(got) != 0 {
		t.Fatalf("registry = %+v, want the entry a later build needs left unread", got)
	}
	e.stdout.Reset()

	_, out := e.doctor()

	if !strings.Contains(out, "every session file of this project is registered") {
		t.Errorf("doctor = %q, want an entry a later build needs counted as registered", out)
	}
}

func TestStatusAndDoctorSayHowManyEntryFilesOfTheRegistryCannotBeRead(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	e.enable(proxytest.WithProxy)
	broken := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(-48*time.Hour))
	e.registerFile(e.canonicalRoot(), broken, "")
	e.sandbox.BreakRegistryEntry(e.status().Hash, broken)
	const line = "Not read: 1 entry file(s) of this project's session file registry could not be read"
	e.stdout.Reset()

	status := e.statusOutput()
	e.stdout.Reset()
	_, doctor := e.doctor()

	if !strings.Contains(status, line) {
		t.Errorf("status = %q, want it to contain %q", status, line)
	}
	if !strings.Contains(doctor, line) {
		t.Errorf("doctor = %q, want it to contain %q", doctor, line)
	}
	if strings.Contains(status, sessionMarker) || strings.Contains(doctor, sessionMarker) {
		t.Error("a surface named the session file of the entry that cannot be read, want a count alone")
	}
}
