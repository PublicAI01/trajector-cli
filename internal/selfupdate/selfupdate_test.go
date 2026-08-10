package selfupdate_test

import (
	"errors"
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

// The platform every test asks about unless it is specifically about
// another one. It is not the host's: which platform an upgrade fetches
// for is an argument, so the whole matrix is reachable from any
// machine.
const (
	testOS   = "linux"
	testArch = "amd64"
)

func source(t *testing.T, s *fakereleases.Server) *selfupdate.Source {
	t.Helper()
	return selfupdate.New(s.IndexURL(), "0.1.0")
}

func TestNewestIsTheHighestVersionNotTheMostRecentlyPublished(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.Publish(t, "0.3.0", []byte("three"))
	releases.Publish(t, "0.2.0", []byte("two"))

	rel, err := source(t, releases).Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if rel.Version != "0.3.0" {
		t.Errorf("newest version is %q, want 0.3.0", rel.Version)
	}
}

func TestNewestFindsAPrereleaseWhenThatIsAllThereIs(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.Publish(t, "0.2.0-rc.1", []byte("two-rc"))

	rel, err := source(t, releases).Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	// Every 0.x release is published as a pre-release. A client that
	// skipped them would report a machine on 0.1.0 as already newest
	// for the whole of the beta.
	if rel.Version != "0.2.0-rc.1" {
		t.Errorf("newest version is %q, want 0.2.0-rc.1", rel.Version)
	}
}

func TestNewestPrefersAFinishedReleaseToItsOwnPrerelease(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "1.0.0", []byte("final"))
	releases.Publish(t, "1.0.0-rc.2", []byte("candidate"))

	rel, err := source(t, releases).Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if rel.Version != "1.0.0" {
		t.Errorf("newest version is %q, want 1.0.0", rel.Version)
	}
}

func TestNewestIgnoresATagThatIsNotAVersion(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishTag(t, "nightly", "nightly", []byte("nightly"))

	rel, err := source(t, releases).Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if rel.Version != "0.1.0" {
		t.Errorf("newest version is %q, want 0.1.0", rel.Version)
	}
}

func TestNewestIgnoresADraftRelease(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishDraft(t, "0.9.0", []byte("unpublished"))

	rel, err := source(t, releases).Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if rel.Version != "0.1.0" {
		t.Errorf("newest version is %q, want 0.1.0", rel.Version)
	}
}

func TestNewestIgnoresAReleaseThisPlatformHasNoArchiveIn(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishWithout(t, "0.2.0", []byte("two"),
		fakereleases.ArchiveName("0.2.0", testOS, testArch))

	rel, err := source(t, releases).Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if rel.Version != "0.1.0" {
		t.Errorf("newest version is %q, want 0.1.0", rel.Version)
	}
}

func TestNewestIgnoresAReleaseWithNoChecksumFile(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.PublishWithout(t, "0.2.0", []byte("two"), "trajector_checksums.txt")

	rel, err := source(t, releases).Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	// An archive nothing can be checked against is not an upgrade this
	// client will make.
	if rel.Version != "0.1.0" {
		t.Errorf("newest version is %q, want 0.1.0", rel.Version)
	}
}

func TestNewestOnASourceThatPublishedNothing(t *testing.T) {
	releases := fakereleases.New(t)

	_, err := source(t, releases).Newest(testOS, testArch)
	if !errors.Is(err, selfupdate.ErrNoRelease) {
		t.Fatalf("error is %v, want ErrNoRelease", err)
	}
}

func TestNewestSaysSoWhenTheSourceIsRationingRequests(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("one"))
	releases.Ration()

	_, err := source(t, releases).Newest(testOS, testArch)
	if err == nil {
		t.Fatal("Newest succeeded against a rationing source")
	}
	// Being rationed is temporary and the user's next move is to wait,
	// which a bare status code does not say.
	if !strings.Contains(err.Error(), "rate limiting") || !strings.Contains(err.Error(), "try again later") {
		t.Errorf("error does not explain rationing: %v", err)
	}
}

func TestNewestSaysSoWhenTheIndexIsNotThere(t *testing.T) {
	releases := fakereleases.New(t)

	_, err := selfupdate.New(releases.IndexURL()+"-elsewhere", "0.1.0").Newest(testOS, testArch)
	if err == nil {
		t.Fatal("Newest succeeded against a source with no index")
	}
	if !strings.Contains(err.Error(), "not published") {
		t.Errorf("error does not say the index is absent: %v", err)
	}
}

func TestDownloadYieldsTheBinaryFromInsideTheArchive(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	src := source(t, releases)

	rel, err := src.Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	binary, err := src.Download(rel, testOS)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	// The archive also carries a LICENSE; taking the first entry rather
	// than the named one would return that instead.
	if string(binary) != "the 0.2.0 binary" {
		t.Errorf("downloaded binary is %q", binary)
	}
}

func TestDownloadYieldsTheBinaryFromInsideAWindowsArchive(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	src := source(t, releases)

	// Windows archives are zips holding trajector.exe. The format and
	// the entry name both differ, and neither depends on the machine
	// this test runs on.
	rel, err := src.Newest("windows", "amd64")
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if !strings.HasSuffix(rel.Archive.Name, "_windows_amd64.zip") {
		t.Fatalf("selected archive is %q", rel.Archive.Name)
	}
	binary, err := src.Download(rel, "windows")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(binary) != "the 0.2.0 binary" {
		t.Errorf("downloaded binary is %q", binary)
	}
}

func TestDownloadRefusesAnArchiveThatDoesNotMatchItsPublishedChecksum(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	releases.Corrupt(t, "0.2.0", testOS, testArch)
	src := source(t, releases)

	rel, err := src.Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	_, err = src.Download(rel, testOS)
	if err == nil {
		t.Fatal("Download accepted an archive that does not match its checksum")
	}
	if !strings.Contains(err.Error(), "checksum") || !strings.Contains(err.Error(), "nothing was installed") {
		t.Errorf("error does not name the mismatch and its consequence: %v", err)
	}
}

func TestDownloadRefusesAnArchiveTheChecksumFileDoesNotCover(t *testing.T) {
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	src := source(t, releases)

	rel, err := src.Newest(testOS, testArch)
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	// A release whose checksum file covers other platforms but not this
	// archive is not a release this client installs on faith.
	rel.Archive.Name = "trajector_0.2.0_plan9_mips.tar.gz"
	if _, err := src.Download(rel, testOS); err == nil {
		t.Fatal("Download accepted an archive with no published checksum")
	} else if !strings.Contains(err.Error(), "refusing to install unverified") {
		t.Errorf("error does not say why it refused: %v", err)
	}
}

func TestDownloadRefusesAReleaseWhoseArchivesAreNotArchives(t *testing.T) {
	for _, c := range []struct{ name, goos string }{
		{"tar.gz", testOS},
		{"zip", "windows"},
	} {
		t.Run(c.name, func(t *testing.T) {
			releases := fakereleases.New(t)
			releases.PublishBroken(t, "0.2.0")
			src := source(t, releases)

			rel, err := src.Newest(c.goos, testArch)
			if err != nil {
				t.Fatalf("Newest: %v", err)
			}
			// The checksum matches — the pipeline uploaded these bytes
			// — so nothing before unpacking can notice.
			if _, err := src.Download(rel, c.goos); err == nil {
				t.Fatal("Download accepted a release whose archive is not an archive")
			} else if !strings.Contains(err.Error(), "reading the release archive") {
				t.Errorf("error does not say what could not be read: %v", err)
			}
		})
	}
}

func TestNewestSaysSoWhenTheIndexIsNotAReleaseList(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>this is a login page, not a release index</html>"))
	}))
	defer elsewhere.Close()

	_, err := selfupdate.New(elsewhere.URL+"/releases", "0.1.0").Newest(testOS, testArch)
	if err == nil {
		t.Fatal("Newest read releases out of a page that lists none")
	}
	if !strings.Contains(err.Error(), "reading the release index") {
		t.Errorf("error does not say what could not be read: %v", err)
	}
}

func TestASourceReachedOverPlaintextIsRefused(t *testing.T) {
	_, err := selfupdate.New("http://releases.example.com/releases", "0.1.0").Newest(testOS, testArch)
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
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://releases.example.com/releases", http.StatusFound)
	}))
	defer redirector.Close()

	// Vetting only the first hop would let a source that answers safely
	// hand the actual bytes off to one that does not.
	_, err := selfupdate.New(redirector.URL+"/releases", "0.1.0").Newest(testOS, testArch)
	if err == nil {
		t.Fatal("a redirect to a plaintext non-loopback source was followed")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error does not say what the source must be: %v", err)
	}
}

func TestTheReleaseSourceNamesThisBuildToTheServer(t *testing.T) {
	var agents []string
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.Header.Get("User-Agent"))
		w.Write([]byte("[]"))
	}))
	defer recorder.Close()

	src := selfupdate.New(recorder.URL+"/releases", "9.9.9")
	if _, err := src.Newest(testOS, testArch); !errors.Is(err, selfupdate.ErrNoRelease) {
		t.Fatalf("Newest: %v", err)
	}
	if len(agents) != 1 || agents[0] != "trajector/9.9.9" {
		t.Errorf("release source identified itself as %v", agents)
	}
}

func TestInstallLeavesTheNewBinaryInPlaceAndRunnable(t *testing.T) {
	dir := t.TempDir()
	exec := filepath.Join(dir, "trajector")
	if err := os.WriteFile(exec, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := selfupdate.Install(exec, []byte("the new binary")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := os.ReadFile(exec)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the new binary" {
		t.Errorf("installed content is %q", got)
	}
	info, err := os.Stat(exec)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		// A binary installed without its executable bit is an upgrade
		// that reports success and leaves the machine unable to run
		// trajector at all.
		t.Errorf("installed mode is %v, want 0755", info.Mode().Perm())
	}
}

func TestInstallLeavesNothingBehindBesideTheBinary(t *testing.T) {
	dir := t.TempDir()
	exec := filepath.Join(dir, "trajector")
	if err := os.WriteFile(exec, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := selfupdate.Install(exec, []byte("the new binary")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// On Windows the stepped-aside previous binary may survive if it is
	// still open; nothing else may, and here nothing is running.
	if len(entries) != 1 || entries[0].Name() != "trajector" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("install directory holds %v, want just the binary", names)
	}
}

func TestInstallIntoADirectoryThatIsNotThereChangesNothing(t *testing.T) {
	dir := t.TempDir()
	exec := filepath.Join(dir, "no-such-directory", "trajector")

	err := selfupdate.Install(exec, []byte("the new binary"))
	if err == nil {
		t.Fatal("Install succeeded into a directory that does not exist")
	}
	if !strings.Contains(err.Error(), "cannot write to") {
		t.Errorf("error does not say where it could not write: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("a failed install left %v behind (%v)", entries, err)
	}
}

func TestInstallOverSomethingThatIsNotAFileChangesNothing(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, "trajector")
	if err := os.Mkdir(occupied, 0o755); err != nil {
		t.Fatal(err)
	}

	err := selfupdate.Install(occupied, []byte("the new binary"))
	if err == nil {
		t.Fatal("Install replaced a directory with a binary")
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a failed install left %v behind", names)
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

func TestPackageManagerOfAnInstallation(t *testing.T) {
	for _, c := range []struct {
		name string
		path string
		want string
	}{
		{"homebrew on apple silicon", "/opt/homebrew/Cellar/trajector/0.1.0/bin/trajector", selfupdate.Homebrew},
		{"homebrew on intel macs", "/usr/local/Cellar/trajector/0.1.0/bin/trajector", selfupdate.Homebrew},
		{"homebrew on linux", "/home/linuxbrew/.linuxbrew/Cellar/trajector/0.1.0/bin/trajector", selfupdate.Homebrew},
		{"a homebrew cask", "/opt/homebrew/Caskroom/trajector/0.1.0/trajector", selfupdate.Homebrew},
		{"scoop app", `C:\Users\dana\scoop\apps\trajector\current\trajector.exe`, selfupdate.Scoop},
		{"scoop shim", `C:\Users\dana\scoop\shims\trajector.exe`, selfupdate.Scoop},
		{"install.sh", "/home/dana/.local/bin/trajector", ""},
		{"a system-wide manual install", "/usr/local/bin/trajector", ""},
		{"go install", "/home/dana/go/bin/trajector", ""},
		{"a build in a checkout", "/home/dana/src/trajector-cli/trajector", ""},
		{"a directory that merely says scoop", "/home/dana/scoop-notes/trajector", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := selfupdate.PackageManager(c.path); got != c.want {
				t.Errorf("PackageManager(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

func TestPackageManagerLooksThroughTheLinkOnThePath(t *testing.T) {
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

	if got := selfupdate.PackageManager(link); got != selfupdate.Homebrew {
		t.Errorf("PackageManager(%q) = %q, want %q", link, got, selfupdate.Homebrew)
	}
}

func TestHostPlatformIsTheMachineThisRunsOn(t *testing.T) {
	goos, goarch := selfupdate.HostPlatform()
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		t.Errorf("HostPlatform() = %q/%q, want %q/%q", goos, goarch, runtime.GOOS, runtime.GOARCH)
	}
}
