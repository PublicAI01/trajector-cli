package selfupdate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// scriptRelease is a 0.2.0 release whose binary is a shell script: it
// answers `version` as 0.2.0, as a release that passes the first run
// does, and runs verify and finish for the two upgrade steps the build
// being installed is asked to take.
func scriptRelease(t *testing.T, verify, finish string) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a script is not a program on Windows")
	}
	return []byte("#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"version) echo trajector 0.2.0 ;;\n" +
		"'hook upgrade-verify') " + verify + " ;;\n" +
		"'hook upgrade-finish') " + finish + " ;;\n" +
		"*) exit 64 ;;\n" +
		"esac\n")
}

// upgradeTo publishes release as 0.2.0 and runs an upgrade of a
// machine on 0.1.0, whose binary is a stand-in file, to it under ctx.
func upgradeTo(ctx context.Context, t *testing.T, release []byte) (installed, error) {
	t.Helper()
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", release)
	_, err := selfupdate.Upgrade(ctx, m.at("0.1.0"), releases.IndexURL(), 0)
	return m, err
}

// checkPutBack checks what every upgrade whose new build did not
// accept its install owes the machine: an error, the 0.1.0 binary back
// under its own name, and nothing left beside it.
func (m installed) checkPutBack(err error) {
	m.t.Helper()
	if err == nil {
		m.t.Fatal("a release that did not accept its install was installed")
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		m.t.Errorf("the binary is %q, want the previous one put back", got)
	}
	if got := m.siblings(); len(got) != 1 {
		m.t.Errorf("a refused upgrade left %v in the install directory", got)
	}
}

func TestUpgrade_ReleaseThatRefusesItselfIsPutBack(t *testing.T) {
	m, err := upgradeTo(t.Context(), t, scriptRelease(t, "exit 3", "exit 0"))
	m.checkPutBack(err)
	unusable, ok := errors.AsType[*selfupdate.UnusableError](err)
	if !ok || unusable.Reason != "its proxy does not start here: it exited with exit status 3" {
		t.Fatalf("Upgrade = %v, want 0.2.0 refused with the exit status it gave", err)
	}
	if state := loadState(t, m.at("0.1.0")); state.Unusable["0.2.0"] != unusable.Reason {
		t.Errorf("unusable versions are %v, want 0.2.0 recorded", state.Unusable)
	}
}

func TestUpgrade_ReleaseWhoseCheckDoesNotEndIsStoppedAndPutBack(t *testing.T) {
	start := time.Now()
	m, err := upgradeTo(t.Context(), t, scriptRelease(t, "exec sleep 60", "exit 0"))
	m.checkPutBack(err)
	unusable, ok := errors.AsType[*selfupdate.UnusableError](err)
	if !ok || unusable.Reason != "its proxy did not start within 20s" {
		t.Fatalf("Upgrade = %v, want 0.2.0 refused for the time it took", err)
	}
	if waited := time.Since(start); waited > 40*time.Second {
		t.Errorf("the upgrade waited %s on a check that does not end", waited)
	}
}

func TestUpgrade_StoppedWhileTheNewBuildChecksItselfPutsThePreviousBinaryBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	m, err := upgradeTo(ctx, t, scriptRelease(t, "exec sleep 60", "exit 0"))
	m.checkPutBack(err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Upgrade = %v, want the stop it was given", err)
	}
	// The release never got its answer; it is not one that does not
	// start here.
	if state := loadState(t, m.at("0.1.0")); len(state.Unusable) != 0 {
		t.Errorf("unusable versions are %v after an upgrade that was stopped", state.Unusable)
	}
}

func TestUpgrade_ReleaseThatRefusesItselfWhereThePreviousBinaryCannotGoBackSaysWhereItIs(t *testing.T) {
	// The new build refuses itself and leaves a directory with something
	// in it where the binary was, so the kept binary cannot be renamed
	// back over it.
	m, err := upgradeTo(t.Context(), t, scriptRelease(t, `rm -f "$0" && mkdir "$0" && : > "$0/held"; exit 3`, "exit 0"))
	if err == nil {
		t.Fatal("a release that did not accept its install was installed")
	}
	kept := m.path + ".old-0.1.0" + filepath.Ext(m.path)
	if _, ok := errors.AsType[*selfupdate.UnusableError](err); ok {
		t.Errorf("Upgrade = %v, an error that says the previous binary is in place", err)
	}
	if strings.Contains(err.Error(), "nothing was installed") {
		t.Errorf("Upgrade = %v, an error that says nothing was installed", err)
	}
	if !strings.Contains(err.Error(), kept) {
		t.Errorf("Upgrade = %v, want it to say the previous binary is at %s", err, kept)
	}
	if data, readErr := os.ReadFile(kept); readErr != nil || string(data) != "the 0.1.0 binary" {
		t.Errorf("the kept binary is %q, %v; want the 0.1.0 binary", data, readErr)
	}
	if s := loadState(t, m.at("0.1.0")); len(s.Unusable) != 0 {
		t.Errorf("unusable versions are %v, want nothing recorded against a release still in place", s.Unusable)
	}
}

func TestUpgrade_CommitNamesTheInstallUnfinishedUntilTheNewBuildFinishesIt(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	record := filepath.Join(t.TempDir(), "record")
	state := m.at("0.1.0").State
	// What each step was run as, and — when the new build finishes —
	// whether the 0.1.0 binary is still kept and what the state file
	// says.
	release := scriptRelease(t,
		"echo \"verify $0\" >> '"+record+"'",
		"{ echo \"finish $0\"; [ -e \"$0.old-0.1.0\" ] && echo kept; cat '"+state+"'; } >> '"+record+"'",
	)
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", release)

	out, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0)
	if err != nil || out.Kind != selfupdate.Upgraded {
		t.Fatalf("Upgrade = %+v, %v; want 0.2.0 installed", out, err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	// Both steps ran from the path the upgrade was given, the one the
	// installation is reached by.
	if !strings.HasPrefix(got, "verify "+m.path+"\nfinish "+m.path+"\n") {
		t.Errorf("the new build ran as:\n%s\nwant verify, then finish, both from %s", got, m.path)
	}
	// By the time the new build finishes, its install is committed —
	// nothing is kept to put back — and the state file already names it
	// as unfinished, so a finish that never ends still leaves a trace.
	if strings.Contains(got, "\nkept\n") {
		t.Errorf("the previous binary was still kept when the new build finished:\n%s", got)
	}
	if !strings.Contains(got, `"finish_pending": "0.2.0"`) {
		t.Errorf("the state file did not name 0.2.0 unfinished while it finished:\n%s", got)
	}
	if s := loadState(t, m.at("0.2.0")); s.Installed != "0.2.0" || s.FinishPending != "" {
		t.Errorf("state is %+v, want 0.2.0 installed and nothing left to finish", s)
	}
}

func TestUpgrade_FinishThatFailsLeavesTheInstallNamedUnfinished(t *testing.T) {
	release := scriptRelease(t, "exit 0", "exit 1")
	m, err := upgradeTo(t.Context(), t, release)
	// The install is committed before the new build finishes it, so a
	// finish that fails is not an upgrade that failed.
	if err != nil {
		t.Fatalf("Upgrade = %v; want 0.2.0 installed", err)
	}
	if m.content() != string(release) {
		t.Error("the 0.2.0 build is not the installed binary")
	}
	if got := m.siblings(); len(got) != 1 {
		t.Errorf("a committed upgrade left %v in the install directory", got)
	}
	if s := loadState(t, m.at("0.2.0")); s.Installed != "0.2.0" || s.FinishPending != "0.2.0" || s.FinishFailures != 1 || s.Error != "" {
		t.Errorf("state is %+v, want 0.2.0 installed and still to be finished after one failure", s)
	}
}

func TestUpgradeThatCannotKeepThePreviousBinaryChangesNothing(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	// Something that is not ours to remove holds the name the 0.1.0
	// binary would be kept under.
	occupied := filepath.Join(m.path+".old-0.1.0"+filepath.Ext(m.path), "somebody's file")
	if err := os.MkdirAll(filepath.Dir(occupied), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(occupied, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", fakereleases.BuildRelease(t, "0.2.0"))

	_, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0)
	if err == nil || !strings.Contains(err.Error(), "keeping the previous binary") {
		t.Fatalf("Upgrade = %v, want the previous binary named as what could not be kept", err)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("the binary is %q, want it untouched", got)
	}
	if _, err := os.Stat(occupied); err != nil {
		t.Errorf("what held the name is gone: %v", err)
	}
	if got := m.siblings(); len(got) != 2 {
		t.Errorf("the install directory holds %v, want the binary and what was already there", got)
	}
}

// kept is the name an upgrade keeps this machine's binary under while
// the build replacing it, which announces version, checks itself.
func (m installed) kept(version string) string {
	return m.path + ".old-" + version + filepath.Ext(m.path)
}

// plant writes content at path, last modified at modified.
func plant(t *testing.T, path, content string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestSweepPutsBackTheBinaryAnInterruptedUpgradeKept(t *testing.T) {
	// The disk as an upgrade leaves it when it dies after putting the
	// 0.2.0 build in place and before that build accepted the install:
	// the build at the binary's path, the 0.1.0 binary kept beside it,
	// and no lock held.
	m := install(t, "the 0.2.0 build, never checked")
	plant(t, m.kept("0.1.0"), "the 0.1.0 binary", time.Now())

	selfupdate.SweepResidue(m.at("0.2.0"))

	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("the binary is %q, want the kept 0.1.0 binary put back", got)
	}
	if got := m.siblings(); len(got) != 1 {
		t.Errorf("the sweep left %v in the install directory", got)
	}
	// The build it replaced was never judged, so nothing is held
	// against it: the next upgrade tries it again.
	if s := loadState(t, m.at("0.1.0")); len(s.Unusable) != 0 {
		t.Errorf("unusable versions are %v after putting back an interrupted upgrade", s.Unusable)
	}
}

func TestSweepPutsBackTheLatestOfSeveralKeptBinaries(t *testing.T) {
	m := install(t, "the 0.3.0 build, never checked")
	now := time.Now()
	plant(t, m.kept("0.2.0"), "the 0.2.0 binary", now)
	plant(t, m.kept("0.1.0"), "the 0.1.0 binary", now.Add(-time.Hour))

	selfupdate.SweepResidue(m.at("0.3.0"))

	if got := m.content(); got != "the 0.2.0 binary" {
		t.Errorf("the binary is %q, want the one written last put back", got)
	}
	if got := m.siblings(); len(got) != 1 {
		t.Errorf("the sweep left %v in the install directory", got)
	}
}

func TestSweepAfterAnInterruptedCommitNamesNothingUnfinished(t *testing.T) {
	// The upgrade died after its 0.2.0 build accepted the install and
	// the state file named 0.2.0 unfinished, before the kept binary
	// went. That build is no longer the one installed once 0.1.0 is
	// back, so there is nothing of its install to finish.
	m := install(t, "the 0.2.0 build")
	plant(t, m.kept("0.1.0"), "the 0.1.0 binary", time.Now())
	writeState(t, m.at("0.1.0"), `{"finish_pending": "0.2.0", "installed": "0.1.0"}`)

	selfupdate.SweepResidue(m.at("0.2.0"))

	if s := loadState(t, m.at("0.1.0")); s.FinishPending != "" || s.Installed != "0.1.0" {
		t.Errorf("state is %+v, want nothing left to finish", s)
	}
}

func TestSweepKeepsWhatTheBinaryPutBackStillHasToFinish(t *testing.T) {
	// 0.1.0 was installed but did not finish; an upgrade from it then
	// died before its new build was checked. 0.1.0 still has to finish.
	m := install(t, "the 0.2.0 build, never checked")
	plant(t, m.kept("0.1.0"), "the 0.1.0 binary", time.Now())
	writeState(t, m.at("0.1.0"), `{"finish_pending": "0.1.0", "installed": "0.1.0"}`)

	selfupdate.SweepResidue(m.at("0.2.0"))

	if s := loadState(t, m.at("0.1.0")); s.FinishPending != "0.1.0" {
		t.Errorf("finish_pending is %q, want 0.1.0 still to be finished", s.FinishPending)
	}
}

func TestUpgrade_CommitNamesTheNewBuildInstalledBeforeItFinishes(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	record := filepath.Join(t.TempDir(), "record")
	release := scriptRelease(t, "exit 0", "cat '"+m.at("0.1.0").State+"' > '"+record+"'; exit 1")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", release)

	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0); err != nil {
		t.Fatalf("Upgrade = %v; want 0.2.0 installed", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"installed": "0.2.0"`) {
		t.Errorf("while the new build finished, the state file was:\n%s\nwant 0.2.0 named installed", data)
	}
}
