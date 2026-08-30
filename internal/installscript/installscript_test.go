// Package installscript has no code. It is where install.sh — the
// shipped one-line installer, the first thing a new user runs — is
// driven end to end against a local release source, so the script is
// covered by the same suite as the binary it installs instead of only
// by whoever last ran it by hand.
//
// Nothing here reads the script's source or reimplements its steps: a
// real sh runs the real file, fetching real archives over HTTP and
// verifying real checksums, and the tests assert on what a user would
// see and on what ends up on disk.
//
// Choosing which release to install is written twice — once in the
// script, which cannot call trajector to do it, and once in the client
// the binary upgrades itself with. Both are put the same questions
// here, against the same release source, so a policy that changes on
// one side alone fails rather than drifts.
package installscript

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// hostArchive is the asset the script picks on the machine running the
// test. Which platform is exercised is the host's, not an argument:
// the script reads uname, and the point of these tests is that it
// reads it correctly.
func hostArchive(version string) string {
	return fakereleases.ArchiveName(version, runtime.GOOS, runtime.GOARCH)
}

// launch runs install.sh the way a user's shell does, pointed at a
// local release source. The environment is built from scratch rather
// than inherited so a variable set on the developer's machine cannot
// change what is tested.
func launch(t *testing.T, releases *fakereleases.Server, env map[string]string) (output string, code int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh installs on macOS and Linux; Windows is told no build is published")
	}
	if !haveDownloader() {
		t.Skip("neither curl nor wget is available for the script to download with")
	}

	full := map[string]string{
		"PATH":               os.Getenv("PATH"),
		"HOME":               t.TempDir(),
		"TRAJECTOR_API_BASE": releases.APIBase(),
		"TRAJECTOR_DL_BASE":  releases.AssetBase(),
	}
	for name, value := range env {
		full[name] = value
	}

	cmd := exec.Command("sh", scriptPath(t))
	cmd.Env = nil
	for name, value := range full {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("running install.sh: %v", err)
	}
	t.Logf("install.sh exited %d:\n%s", code, out)
	return string(out), code
}

func haveDownloader() bool {
	for _, tool := range []string{"curl", "wget"} {
		if _, err := exec.LookPath(tool); err == nil {
			return true
		}
	}
	return false
}

func scriptPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("locating install.sh: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("install.sh is not where the tests expect it: %v", err)
	}
	return path
}

// fakeUname returns a directory to put ahead of PATH, holding a uname
// that answers for a machine other than this one — the only way to
// reach the platforms whose branch is a refusal.
func fakeUname(t *testing.T, kernel, machine string) string {
	t.Helper()
	dir := t.TempDir()
	body := fmt.Sprintf("#!/bin/sh\ncase $1 in\n-s) echo '%s' ;;\n-m) echo '%s' ;;\nesac\n", kernel, machine)
	if err := os.WriteFile(filepath.Join(dir, "uname"), []byte(body), 0o755); err != nil {
		t.Fatalf("writing the stand-in uname: %v", err)
	}
	return dir
}

// contents is what lies in a directory, for asserting that a refused
// install left nothing at all — neither a binary nor a staged file.
func contents(t *testing.T, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var names []string
	for _, entry := range found {
		names = append(names, entry.Name())
	}
	return names
}

func installed(t *testing.T, dir string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "trajector"))
	if err != nil {
		t.Fatalf("reading the installed binary: %v", err)
	}
	return body
}

func TestTheScriptInstallsTheNewestReleaseAndSaysSo(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.Publish(t, "0.2.0", []byte("two"))
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir})
	if code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if got := installed(t, dir); string(got) != "two" {
		t.Errorf("installed binary is %q, want the 0.2.0 build", got)
	}
	if !strings.Contains(out, "Installed trajector 0.2.0 in "+dir) {
		t.Errorf("install.sh did not report what it installed:\n%s", out)
	}
	// Only this platform's archive and the checksum file are worth
	// fetching; a script that pulled the other five would still pass
	// every other assertion here.
	want := []string{hostArchive("0.2.0"), fakereleases.ChecksumsAsset}
	if got := releases.Downloads(); !equal(got, want) {
		t.Errorf("downloaded %v, want %v", got, want)
	}
}

func TestTheInstalledBinaryIsExecutable(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	dir := t.TempDir()

	if _, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir}); code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	info, err := os.Stat(filepath.Join(dir, "trajector"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary has mode %v; nobody can run it", info.Mode().Perm())
	}
}

func TestTheHighestVersionWinsOverTheMostRecentlyPublished(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	// A patch to the older line, published afterwards: it heads the
	// index, and a script that took the first entry would hand every
	// new install a binary older than the one already out.
	releases.Publish(t, "0.1.1", []byte("one-one"))
	dir := t.TempDir()

	if _, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir}); code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if got := installed(t, dir); string(got) != "two" {
		t.Errorf("installed binary is %q, want the 0.2.0 build", got)
	}
}

// installer is one of the two ways a machine moves to a published
// release: the shell script a new user pipes into sh, and the client
// the installed binary upgrades itself with. They cannot share code —
// the script runs where no trajector exists yet — so the release
// source is where they are held to the same answer.
type installer struct {
	name string
	// picked is the version this installer settles on, given a release
	// source to choose from.
	picked func(t *testing.T, releases *fakereleases.Server) string
}

var installers = []installer{
	{"the install script", pickedByScript},
	{"the upgrade client", pickedByClient},
}

func pickedByScript(t *testing.T, releases *fakereleases.Server) string {
	t.Helper()
	dir := t.TempDir()
	out, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir})
	if code != 0 {
		t.Fatalf("install.sh exited %d, want 0:\n%s", code, out)
	}
	return reportedVersion(t, out)
}

func pickedByClient(t *testing.T, releases *fakereleases.Server) string {
	t.Helper()
	// The client replaces the binary it is pointed at, so it is given
	// one: a file standing in for an installation older than anything
	// these tests publish.
	execPath := filepath.Join(t.TempDir(), "trajector")
	if err := os.WriteFile(execPath, []byte("an older build"), 0o755); err != nil {
		t.Fatalf("planting the installed binary: %v", err)
	}
	out, err := selfupdate.Upgrade(execPath, "0.0.1", releases.IndexURL())
	if err != nil {
		t.Fatalf("the upgrade client installed nothing: %v", err)
	}
	if out.Kind != selfupdate.Upgraded {
		t.Fatalf("the upgrade client left %s installed rather than moving to a release", out.From)
	}
	return out.To
}

// reportedVersion is the version install.sh says it put on the
// machine, read back out of what a user sees rather than out of the
// binary, so an installer that installs one build and announces
// another is caught too.
func reportedVersion(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "Installed trajector ")
		if !ok {
			continue
		}
		if version, _, ok := strings.Cut(rest, " in "); ok {
			return version
		}
	}
	t.Fatalf("install.sh reported no installed version:\n%s", out)
	return ""
}

func TestADraftIsPassedOverEvenWhenItNamesTheHighestVersion(t *testing.T) {
	for _, inst := range installers {
		t.Run(inst.name, func(t *testing.T) {
			releases := fakereleases.New(t)
			releases.Publish(t, "0.2.0", []byte("two"))
			// Prepared and tagged but never published: its archives
			// cannot be downloaded, so an installer that picks it
			// hands the user a failed install instead of an upgrade.
			releases.PublishDraft(t, "0.9.0", []byte("unpublished"))

			if got := inst.picked(t, releases); got != "0.2.0" {
				t.Errorf("installed %s, want 0.2.0", got)
			}
		})
	}
}

func TestACandidatePublishedAfterItsFinishedVersionDoesNotWin(t *testing.T) {
	for _, inst := range installers {
		t.Run(inst.name, func(t *testing.T) {
			releases := fakereleases.New(t)
			releases.Publish(t, "0.3.0", []byte("three"))
			// Cut after the version it was a candidate for, which
			// happens whenever a candidate is tagged late. It heads
			// the index, and precedence still puts it below the
			// finished version it precedes.
			releases.Publish(t, "0.3.0-rc1", []byte("three-rc"))

			if got := inst.picked(t, releases); got != "0.3.0" {
				t.Errorf("installed %s, want 0.3.0", got)
			}
		})
	}
}

func TestATagThatIsNotAVersionIsPassedOver(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishTag(t, "nightly", "nightly", []byte("nightly"))
	dir := t.TempDir()

	if _, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir}); code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if got := installed(t, dir); string(got) != "one" {
		t.Errorf("installed binary is %q, want the 0.1.0 build", got)
	}
}

func TestAPinnedVersionIsInstalledInsteadOfTheNewest(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.Publish(t, "0.2.0", []byte("two"))
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{
		"TRAJECTOR_INSTALL_DIR": dir,
		"TRAJECTOR_VERSION":     "v0.1.0",
	})
	if code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if got := installed(t, dir); string(got) != "one" {
		t.Errorf("installed binary is %q, want the 0.1.0 build", got)
	}
	if !strings.Contains(out, "Installed trajector 0.1.0") {
		t.Errorf("install.sh did not report the pinned version:\n%s", out)
	}
}

func TestTheDefaultInstallDirectoryIsUnderTheHomeDirectory(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	home := t.TempDir()

	// The README promises ~/.local/bin. Nothing else in the script
	// would notice if that moved.
	if _, code := launch(t, releases, map[string]string{"HOME": home}); code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if got := installed(t, filepath.Join(home, ".local", "bin")); string(got) != "two" {
		t.Errorf("installed binary is %q, want the 0.2.0 build", got)
	}
}

func TestAnArchiveThatFailsVerificationInstallsNothing(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	// What arrives no longer hashes to what the release published:
	// a tampered mirror, or a download that ended early.
	releases.Corrupt(t, "0.2.0", runtime.GOOS, runtime.GOARCH)
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir})
	if code == 0 {
		t.Fatal("install.sh exited 0 after a checksum mismatch")
	}
	if !strings.Contains(out, "checksum mismatch for "+hostArchive("0.2.0")) {
		t.Errorf("install.sh did not say the checksum did not match:\n%s", out)
	}
	// Not even a staged file: the destination directory is as it was.
	if left := contents(t, dir); len(left) != 0 {
		t.Errorf("a refused install left %v behind", left)
	}
}

func TestAVerificationFailureLeavesTheBinaryAlreadyInstalledAlone(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	releases.Corrupt(t, "0.2.0", runtime.GOOS, runtime.GOARCH)
	dir := t.TempDir()
	existing := []byte("#!/bin/sh\necho 'trajector 0.1.0'\n")
	if err := os.WriteFile(filepath.Join(dir, "trajector"), existing, 0o755); err != nil {
		t.Fatalf("planting the installed binary: %v", err)
	}

	if _, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir}); code == 0 {
		t.Fatal("install.sh exited 0 after a checksum mismatch")
	}
	if got := installed(t, dir); string(got) != string(existing) {
		t.Errorf("the working binary was changed to %q", got)
	}
	if left := contents(t, dir); len(left) != 1 {
		t.Errorf("the install directory holds %v, want only the binary that was already there", left)
	}
}

func TestAReleaseWithNoChecksumFileInstallsNothing(t *testing.T) {
	releases := fakereleases.New(t)
	releases.PublishWithout(t, "0.2.0", []byte("two"), fakereleases.ChecksumsAsset)
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir})
	if code == 0 {
		t.Fatal("install.sh exited 0 with nothing to verify against")
	}
	// Installing unverified is not an option the script offers.
	if !strings.Contains(out, "refusing to install unverified") {
		t.Errorf("install.sh did not refuse in the documented words:\n%s", out)
	}
	if left := contents(t, dir); len(left) != 0 {
		t.Errorf("a refused install left %v behind", left)
	}
}

func TestAReleaseMissingThisPlatformsArchiveInstallsNothing(t *testing.T) {
	releases := fakereleases.New(t)
	releases.PublishWithout(t, "0.2.0", []byte("two"), hostArchive("0.2.0"))
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir})
	if code == 0 {
		t.Fatal("install.sh exited 0 for a release it could not download")
	}
	if !strings.Contains(out, "has no asset "+hostArchive("0.2.0")) {
		t.Errorf("install.sh did not name the missing asset:\n%s", out)
	}
	if left := contents(t, dir); len(left) != 0 {
		t.Errorf("a refused install left %v behind", left)
	}
}

func TestReplacingAnInstalledBinaryReportsBothVersions(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	dir := t.TempDir()
	existing := "#!/bin/sh\necho 'trajector 0.1.0'\n"
	if err := os.WriteFile(filepath.Join(dir, "trajector"), []byte(existing), 0o755); err != nil {
		t.Fatalf("planting the installed binary: %v", err)
	}

	out, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir})
	if code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if !strings.Contains(out, "Replaced trajector 0.1.0 with trajector 0.2.0 in "+dir) {
		t.Errorf("install.sh did not report the replacement:\n%s", out)
	}
	if got := installed(t, dir); string(got) != "two" {
		t.Errorf("installed binary is %q, want the 0.2.0 build", got)
	}
}

func TestAnInstallDirectoryOffThePathComesWithInstructions(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{"TRAJECTOR_INSTALL_DIR": dir})
	if code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if !strings.Contains(out, dir+" is not on your PATH") {
		t.Errorf("install.sh installed somewhere unreachable without saying so:\n%s", out)
	}
	if !strings.Contains(out, `export PATH="`+dir+`:$PATH"`) {
		t.Errorf("install.sh did not say how to fix the PATH:\n%s", out)
	}
}

func TestAnInstallDirectoryAlreadyOnThePathIsNotCommentedOn(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{
		"TRAJECTOR_INSTALL_DIR": dir,
		"PATH":                  dir + ":" + os.Getenv("PATH"),
	})
	if code != 0 {
		t.Fatalf("install.sh exited %d, want 0", code)
	}
	if strings.Contains(out, "is not on your PATH") {
		t.Errorf("install.sh told a user to fix a PATH that was already right:\n%s", out)
	}
}

func TestWindowsIsToldNoBuildIsPublishedAndDownloadsNothing(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	dir := t.TempDir()

	out, code := launch(t, releases, map[string]string{
		"TRAJECTOR_INSTALL_DIR": dir,
		"PATH":                  fakeUname(t, "MINGW64_NT-10.0", "x86_64") + ":" + os.Getenv("PATH"),
	})
	// Non-zero because nothing was installed: a caller chaining off
	// this script must not read printed advice as success.
	if code == 0 {
		t.Error("install.sh exited 0 on Windows without installing anything")
	}
	// What a Windows user needs is the fact that there is no build and
	// the one way to run trajector anyway. An archive name would be a
	// download that 404s.
	for _, want := range []string{"does not publish a Windows build", "WSL"} {
		if !strings.Contains(out, want) {
			t.Errorf("the Windows message does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".zip") {
		t.Errorf("the Windows message names an archive that is not published:\n%s", out)
	}
	if got := releases.Downloads(); len(got) != 0 {
		t.Errorf("the Windows branch downloaded %v", got)
	}
	if left := contents(t, dir); len(left) != 0 {
		t.Errorf("the Windows branch left %v behind", left)
	}
}

func TestAPlatformWithNoBuildIsRefusedBeforeAnythingIsFetched(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kernel  string
		machine string
		want    string
	}{
		{"an architecture nothing is built for", "Linux", "riscv64", "unsupported architecture: riscv64"},
		{"an operating system nothing is built for", "SunOS", "x86_64", "unsupported operating system: SunOS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			releases := fakereleases.New(t)
			releases.Publish(t, "0.2.0", []byte("two"))
			dir := t.TempDir()

			out, code := launch(t, releases, map[string]string{
				"TRAJECTOR_INSTALL_DIR": dir,
				"PATH":                  fakeUname(t, tc.kernel, tc.machine) + ":" + os.Getenv("PATH"),
			})
			if code == 0 {
				t.Error("install.sh exited 0 for a platform it has no build for")
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("install.sh did not explain the refusal as %q:\n%s", tc.want, out)
			}
			if got := releases.Downloads(); len(got) != 0 {
				t.Errorf("a refused platform downloaded %v", got)
			}
			if left := contents(t, dir); len(left) != 0 {
				t.Errorf("a refused platform left %v behind", left)
			}
		})
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
