package selfupdate_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// installed is one machine's binary: a file standing in for the running
// program, at a path an upgrade may replace.
type installed struct {
	t    *testing.T
	dir  string
	path string
}

// install puts content where this machine's binary lives.
func install(t *testing.T, content string) installed {
	t.Helper()
	dir := t.TempDir()
	m := installed{t: t, dir: dir, path: filepath.Join(dir, "trajector")}
	if err := os.WriteFile(m.path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return m
}

// content is what is on disk where this machine's binary lives.
func (m installed) content() string {
	m.t.Helper()
	data, err := os.ReadFile(m.path)
	if err != nil {
		m.t.Fatalf("the binary is gone: %v", err)
	}
	return string(data)
}

// siblings is every file in the directory the binary lives in.
func (m installed) siblings() []string {
	m.t.Helper()
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		m.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// hostArchive is the asset name this machine's upgrade looks for in a
// release: which platform is fetched is the host's, not an argument.
func hostArchive(version string) string {
	return fakereleases.ArchiveName(version, runtime.GOOS, runtime.GOARCH)
}

func TestUpgradeInstallsTheHighestVersionNotTheMostRecentlyPublished(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.Publish(t, "0.3.0", []byte("three"))
	releases.Publish(t, "0.2.0", []byte("two"))

	out, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.Upgraded || out.From != "0.1.0" || out.To != "0.3.0" {
		t.Errorf("outcome is %+v, want an upgrade from 0.1.0 to 0.3.0", out)
	}
	// The archive also carries a LICENSE; taking the first entry rather
	// than the named one would install that instead.
	if got := m.content(); got != "three" {
		t.Errorf("installed binary is %q", got)
	}
}

func TestUpgradeMovesToAPrereleaseWhenThatIsWhatIsPublished(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.Publish(t, "0.2.0-rc.1", []byte("two-rc"))

	out, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	// Every 0.x release is published as a pre-release. A client that
	// skipped them would report a machine on 0.1.0 as already newest for
	// the whole of the beta.
	if out.To != "0.2.0-rc.1" {
		t.Errorf("upgraded to %q, want 0.2.0-rc.1", out.To)
	}
}

func TestUpgradePrefersAFinishedReleaseToItsOwnPrerelease(t *testing.T) {
	m := install(t, "the 0.9.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "1.0.0", []byte("final"))
	releases.Publish(t, "1.0.0-rc.2", []byte("candidate"))

	out, err := selfupdate.Upgrade(m.path, "0.9.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.To != "1.0.0" || m.content() != "final" {
		t.Errorf("upgraded to %q holding %q, want 1.0.0", out.To, m.content())
	}
}

func TestUpgradeIgnoresATagThatIsNotAVersion(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("two"))
	releases.PublishTag(t, "nightly", "nightly", []byte("nightly"))

	out, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.To != "0.2.0" || m.content() != "two" {
		t.Errorf("upgraded to %q holding %q, want 0.2.0", out.To, m.content())
	}
}

func TestUpgradeIgnoresADraftRelease(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishDraft(t, "0.9.0", []byte("unpublished"))

	out, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.AlreadyNewest {
		t.Errorf("outcome is %+v, want a machine already on the newest release", out)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
}

func TestUpgradeIgnoresAReleaseThisPlatformHasNoArchiveIn(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishWithout(t, "0.2.0", []byte("two"), hostArchive("0.2.0"))

	out, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.AlreadyNewest {
		t.Errorf("outcome is %+v, want the half-published release passed over", out)
	}
}

func TestUpgradeIgnoresAReleaseWithNoChecksumFile(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishWithout(t, "0.2.0", []byte("two"), fakereleases.ChecksumsAsset)

	out, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	// An archive nothing can be checked against is not an upgrade this
	// client will make.
	if out.Kind != selfupdate.AlreadyNewest {
		t.Errorf("outcome is %+v, want the uncheckable release passed over", out)
	}
}

func TestUpgradeNeverMovesBackToAnOlderRelease(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	// A release withdrawn after this machine installed it, or a machine
	// running a build from a tag ahead of the source: either way the
	// newest thing published is behind, and behind is not an upgrade.
	releases.Publish(t, "0.0.9", []byte("an older binary"))

	out, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.AlreadyNewest || out.From != "0.1.0" {
		t.Errorf("outcome is %+v, want the older release refused", out)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
	if downloads := releases.Downloads(); len(downloads) != 0 {
		t.Errorf("upgrade downloaded %v with nothing to install", downloads)
	}
}

func TestUpgradeOfABuildThatIsNotAPublishedRelease(t *testing.T) {
	m := install(t, "a build from a checkout")
	releases := fakereleases.New(t)
	releases.Publish(t, "9.9.9", []byte("a much newer binary"))

	out, err := selfupdate.Upgrade(m.path, "dev", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	// A build from a checkout has no place in the version order, and
	// replacing it with a release would discard whatever it was built to
	// test.
	if out.Kind != selfupdate.NotARelease || out.From != "dev" {
		t.Errorf("outcome is %+v, want an unplaceable build", out)
	}
	if got := m.content(); got != "a build from a checkout" {
		t.Errorf("installed binary is %q", got)
	}
	if downloads := releases.Downloads(); len(downloads) != 0 {
		t.Errorf("upgrade downloaded %v for a build it will not replace", downloads)
	}
}

func TestUpgradeOfAnInstallationAPackageManagerOwns(t *testing.T) {
	for _, c := range []struct {
		name string
		path string
		want selfupdate.Manager
	}{
		{"homebrew on apple silicon", "opt/homebrew/Cellar/trajector/0.1.0/bin/trajector", selfupdate.Homebrew},
		{"homebrew on intel macs", "usr/local/Cellar/trajector/0.1.0/bin/trajector", selfupdate.Homebrew},
		{"homebrew on linux", "home/linuxbrew/.linuxbrew/Cellar/trajector/0.1.0/bin/trajector", selfupdate.Homebrew},
		{"a homebrew cask", "opt/homebrew/Caskroom/trajector/0.1.0/trajector", selfupdate.Homebrew},
		{"scoop app", `C:\Users\dana\scoop\apps\trajector\current\trajector.exe`, selfupdate.Scoop},
		{"scoop shim", `C:\Users\dana\scoop\shims\trajector.exe`, selfupdate.Scoop},
		{"install.sh", "home/dana/.local/bin/trajector", ""},
		{"a system-wide manual install", "usr/local/bin/trajector", ""},
		{"go install", "home/dana/go/bin/trajector", ""},
		{"a build in a checkout", "home/dana/src/trajector-cli/trajector", ""},
		{"a directory that merely says scoop", "home/dana/scoop-notes/trajector", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			// A source that has published nothing: an installation nobody
			// manages is proved by the run going on to consult it.
			releases := fakereleases.New(t)
			// The spelling of the path is what is under test, but an
			// upgrade run reads and writes around the binary it is given,
			// so every path is rooted in this test's own directory.
			path := filepath.Join(t.TempDir(), c.path)

			out, err := selfupdate.Upgrade(path, "0.1.0", releases.IndexURL())
			if c.want == "" {
				if err == nil {
					t.Fatalf("outcome is %+v, want the release source consulted", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Upgrade: %v", err)
			}
			if out.Kind != selfupdate.Managed || out.Manager != c.want {
				t.Errorf("outcome is %+v, want %s to own the installation", out, c.want)
			}
			if downloads := releases.Downloads(); len(downloads) != 0 {
				t.Errorf("upgrade downloaded %v for an installation it will not replace", downloads)
			}
		})
	}
}

func TestUpgradeLooksThroughTheLinkOnThePathForTheOwningManager(t *testing.T) {
	// Homebrew installs into its own tree and links the binary onto the
	// user's PATH; the link's own location says nothing about who owns
	// the installation.
	dir := t.TempDir()
	cellar := filepath.Join(dir, "Cellar", "trajector", "0.1.0", "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(cellar, "trajector")
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "trajector")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this machine does not allow symbolic links: %v", err)
	}
	releases := fakereleases.New(t)
	releases.Publish(t, "9.9.9", []byte("a much newer binary"))

	out, err := selfupdate.Upgrade(link, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.Managed || out.Manager != selfupdate.Homebrew {
		t.Errorf("outcome is %+v, want Homebrew to own the installation", out)
	}
}

func TestUpgradeLeavesTheBinaryUntouchedWhenTheDownloadFailsVerification(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	releases.Corrupt(t, "0.2.0", runtime.GOOS, runtime.GOARCH)

	_, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err == nil {
		t.Fatal("Upgrade installed a download that failed verification")
	}
	if !strings.Contains(err.Error(), "checksum") || !strings.Contains(err.Error(), "nothing was installed") {
		t.Errorf("error does not name the mismatch and its consequence: %v", err)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
	// A failed upgrade must not leave a staged binary next to the real
	// one either.
	if got := m.siblings(); len(got) != 1 {
		t.Errorf("a failed upgrade left %v in the install directory", got)
	}
}

func TestUpgradeRefusesAReleaseWhoseArchivesAreNotArchives(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.PublishBroken(t, "0.2.0")

	// The checksum matches — the pipeline uploaded these bytes — so
	// nothing before unpacking can notice.
	_, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err == nil {
		t.Fatal("Upgrade accepted a release whose archive is not an archive")
	}
	if !strings.Contains(err.Error(), "reading the release archive") {
		t.Errorf("error does not say what could not be read: %v", err)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
}

func TestUpgradeSaysSoWhenTheSourceHasPublishedNothing(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)

	_, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err == nil {
		t.Fatal("Upgrade succeeded against a source that has published nothing")
	}
	if !strings.Contains(err.Error(), "no usable version") {
		t.Errorf("error does not say the index named nothing: %v", err)
	}
}

func TestUpgradeSaysSoWhenTheSourceIsRationingRequests(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	releases.Ration()

	_, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL())
	if err == nil {
		t.Fatal("Upgrade succeeded against a rationing source")
	}
	// Being rationed is temporary and the user's next move is to wait,
	// which a bare status code does not say.
	if !strings.Contains(err.Error(), "rate limiting") || !strings.Contains(err.Error(), "try again later") {
		t.Errorf("error does not explain rationing: %v", err)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
}

func TestUpgradeSaysSoWhenTheIndexIsNotThere(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)

	_, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL()+"-elsewhere")
	if err == nil {
		t.Fatal("Upgrade succeeded against a source with no index")
	}
	if !strings.Contains(err.Error(), "not published") {
		t.Errorf("error does not say the index is absent: %v", err)
	}
}

func TestUpgradeSaysSoWhenTheIndexIsNotAReleaseList(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>this is a login page, not a release index</html>"))
	}))
	defer elsewhere.Close()

	_, err := selfupdate.Upgrade(m.path, "0.1.0", elsewhere.URL+"/releases")
	if err == nil {
		t.Fatal("Upgrade read releases out of a page that lists none")
	}
	if !strings.Contains(err.Error(), "reading the release index") {
		t.Errorf("error does not say what could not be read: %v", err)
	}
}

func TestASourceReachedOverPlaintextIsRefused(t *testing.T) {
	m := install(t, "the 0.1.0 binary")

	_, err := selfupdate.Upgrade(m.path, "0.1.0", "http://releases.example.com/releases")
	if err == nil {
		t.Fatal("a plaintext non-loopback release source was accepted")
	}
	// What comes back from here becomes the binary the user runs next,
	// so the connection has to be one somebody authenticated.
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error does not say what the source must be: %v", err)
	}
}

func TestASourceRedirectingToPlaintextIsRefused(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://releases.example.com/releases", http.StatusFound)
	}))
	defer redirector.Close()

	// Vetting only the first hop would let a source that answers safely
	// hand the actual bytes off to one that does not.
	_, err := selfupdate.Upgrade(m.path, "0.1.0", redirector.URL+"/releases")
	if err == nil {
		t.Fatal("a redirect to a plaintext non-loopback source was followed")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error does not say what the source must be: %v", err)
	}
}

func TestTheReleaseSourceNamesThisBuildToTheServer(t *testing.T) {
	m := install(t, "the 9.9.9 binary")
	var agents []string
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.Header.Get("User-Agent"))
		w.Write([]byte("[]"))
	}))
	defer recorder.Close()

	if _, err := selfupdate.Upgrade(m.path, "9.9.9", recorder.URL+"/releases"); err == nil {
		t.Fatal("Upgrade succeeded against a source listing nothing")
	}
	if len(agents) != 1 || agents[0] != "trajector/9.9.9" {
		t.Errorf("release source identified itself as %v", agents)
	}
}

func TestTheInstalledBinaryIsLeftRunnable(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))

	if _, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	info, err := os.Stat(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		// A binary installed without its executable bit is an upgrade
		// that reports success and leaves the machine unable to run
		// trajector at all.
		t.Errorf("installed mode is %v, want 0755", info.Mode().Perm())
	}
	// On Windows the stepped-aside previous binary may survive if it is
	// still open; nothing else may, and here nothing is running.
	if got := m.siblings(); len(got) != 1 || got[0] != "trajector" {
		t.Errorf("install directory holds %v, want just the binary", got)
	}
}

func TestUpgradeIntoADirectoryThatIsNotThereChangesNothing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-directory", "trajector")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))

	_, err := selfupdate.Upgrade(missing, "0.1.0", releases.IndexURL())
	if err == nil {
		t.Fatal("Upgrade succeeded into a directory that does not exist")
	}
	if !strings.Contains(err.Error(), "cannot write to") {
		t.Errorf("error does not say where it could not write: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("a failed upgrade left %v behind (%v)", entries, err)
	}
}

func TestUpgradeOverSomethingThatIsNotAFileChangesNothing(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, "trajector")
	if err := os.Mkdir(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))

	_, err := selfupdate.Upgrade(occupied, "0.1.0", releases.IndexURL())
	if err == nil {
		t.Fatal("Upgrade replaced a directory with a binary")
	}
	// What a rename does over a directory differs between systems, and
	// on one of them it takes the directory away. The refusal has to be
	// ours, not the platform's.
	if !strings.Contains(err.Error(), "is not a file") {
		t.Errorf("error does not say what stood in the way: %v", err)
	}
	if info, err := os.Stat(occupied); err != nil || !info.IsDir() {
		t.Errorf("the directory did not survive: %v, %v", info, err)
	}
	// The staged file must not survive a replacement that could not
	// happen: the next run would sweep it, but until then it is a
	// half-installed binary sitting next to the real one.
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Errorf("a failed upgrade left %v behind (%v)", entries, err)
	}
}

func TestUpgradeSweepsWhatAnInterruptedUpgradeLeftBehind(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	residue := filepath.Join(m.dir, "trajector.old-9f2c")
	if err := os.WriteFile(residue, []byte("a previous binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))

	// Even a run that installs nothing tidies up: on Windows the file an
	// earlier upgrade stepped aside could not be deleted while it was
	// still the running image.
	if _, err := selfupdate.Upgrade(m.path, "0.1.0", releases.IndexURL()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Errorf("residue of an earlier upgrade is still there (%v)", err)
	}
}

func TestSweepResidueRemovesWhatAnEarlierUpgradeLeftBehind(t *testing.T) {
	dir := t.TempDir()
	exec := filepath.Join(dir, "trajector")
	files := map[string]string{
		"trajector":                    "the binary",
		"trajector.old-9f2c":           "a previous binary that could not be deleted while running",
		"trajector.incoming-4a1b":      "a staged binary that never landed",
		"trajector.exe":                "another platform's binary, not this one's residue",
		"trajector-config-backup.json": "somebody else's file",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	selfupdate.SweepResidue(exec)

	left := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		left[e.Name()] = true
	}
	for _, name := range []string{"trajector", "trajector.exe", "trajector-config-backup.json"} {
		if !left[name] {
			t.Errorf("%s was swept away", name)
		}
	}
	for _, name := range []string{"trajector.old-9f2c", "trajector.incoming-4a1b"} {
		if left[name] {
			t.Errorf("%s was left behind", name)
		}
	}
}

func TestSweepingADirectoryThatIsNotThereIsNotAProblem(t *testing.T) {
	// Housekeeping runs before anything has checked the installation is
	// where it is supposed to be, and must not be what fails.
	selfupdate.SweepResidue(filepath.Join(t.TempDir(), "gone", "trajector"))
}
