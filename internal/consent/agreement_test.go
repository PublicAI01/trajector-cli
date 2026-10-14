package consent_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

// agreementVersionFloor is the version the terms before these carried.
// A version that does not pass it leaves every earlier acceptance
// current, so nobody is asked to reconfirm and the pause never fires.
const agreementVersionFloor = "2026-10-13"

// The version, the agreement text, and PRIVACY.md state the same terms.
// The three hashes below pin them to each other: changing any one of the
// three without the other two fails the test.
const (
	pinnedAgreementVersionSHA256 = "cf1ac2a9beae735e35b1444074ecd7837d84c90c6d23124248f8d68577b96438"
	pinnedAgreementTextSHA256    = "63e38ae91bd49711d9f792635a2a7829a7c1bf34a11d644982f2f6a49ad7a452"
	pinnedPrivacyMarkdownSHA256  = "79a6217e659c035a64fff357db49f26cdce40c356370751c906454c95d05c2dd"
)

const privacyMarkdownPath = "../../PRIVACY.md"

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The version is one constant, and the release day it names is stated
// in the agreement text and in PRIVACY.md. This test is what a release
// changing the day has to satisfy: the other two places say the new day
// too, and the day itself still passes the floor.
func TestAgreementVersion_IsAReleaseDaySaidInAllThreePlaces(t *testing.T) {
	version, err := time.Parse(time.DateOnly, consent.AgreementVersion)
	if err != nil {
		t.Fatalf("AgreementVersion %q is not an ISO date (YYYY-MM-DD): %v", consent.AgreementVersion, err)
	}
	floor, err := time.Parse(time.DateOnly, agreementVersionFloor)
	if err != nil {
		t.Fatalf("agreementVersionFloor %q is not an ISO date: %v", agreementVersionFloor, err)
	}
	if !version.After(floor) {
		t.Errorf("AgreementVersion %q does not pass %q: an acceptance of the earlier terms would stay current",
			consent.AgreementVersion, agreementVersionFloor)
	}
	if !strings.Contains(consent.AgreementText, consent.AgreementVersion) {
		t.Errorf("the agreement text does not say the version %q it is shown under", consent.AgreementVersion)
	}
	privacy, err := os.ReadFile(privacyMarkdownPath)
	if err != nil {
		t.Fatalf("read PRIVACY.md: %v", err)
	}
	if !strings.Contains(string(privacy), consent.AgreementVersion) {
		t.Errorf("PRIVACY.md does not say the version %q it describes", consent.AgreementVersion)
	}
}

// updatesSection matches the heading of the agreement's section on
// updates, in the numbered form every section takes.
var updatesSection = regexp.MustCompile(`(?m)^[1-9][0-9]*\. Updates\. `)

// UpdatesSince names the release day the agreement first told the user
// about the automatic upgrade check. A day later than the agreement
// shown would keep every device from checking although the agreement it
// accepted says it does, so an acceptance of the current agreement must
// cover it. A day too early is the other failure, and the worse one: it
// lets an acceptance of terms without the section cover the check.
func TestUpdatesSince_IsNoLaterThanTheAgreementThatCarriesTheSection(t *testing.T) {
	s := consent.Open(filepath.Join(t.TempDir(), "consent.json"))
	if err := s.AcceptAgreement(consent.AgreementVersion, "2026-10-12T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	covers, err := s.CoversUpdates()
	if err != nil {
		t.Fatal(err)
	}
	since, err := time.Parse(time.DateOnly, consent.UpdatesSince)
	if err != nil {
		t.Fatalf("UpdatesSince %q is not an ISO date (YYYY-MM-DD): %v", consent.UpdatesSince, err)
	}
	version, err := time.Parse(time.DateOnly, consent.AgreementVersion)
	if err != nil {
		t.Fatalf("AgreementVersion %q is not an ISO date: %v", consent.AgreementVersion, err)
	}
	if since.After(version) {
		t.Errorf("UpdatesSince %q is later than AgreementVersion %q", consent.UpdatesSince, consent.AgreementVersion)
	}
	if !updatesSection.MatchString(consent.AgreementText) {
		t.Error("the agreement text has no Updates section, but UpdatesSince says it carries one")
	}
	if !covers {
		t.Error("an acceptance of the current agreement does not cover updates")
	}
}

// lastAgreementWithoutUpdates is the last agreement version published
// without the section on updates. An UpdatesSince of that day or earlier
// lets every acceptance of those terms cover a check the user was never
// told about. Once AgreementVersion passes it, UpdatesSince must pass it
// too; it need not follow AgreementVersion further, because a revision
// that leaves the section alone keeps covering the devices that already
// accepted it.
const lastAgreementWithoutUpdates = "2026-09-29"

func TestUpdatesSince_PassesTheLastAgreementWithoutTheSectionOnceTheVersionDoes(t *testing.T) {
	version, err := time.Parse(time.DateOnly, consent.AgreementVersion)
	if err != nil {
		t.Fatalf("AgreementVersion %q is not an ISO date: %v", consent.AgreementVersion, err)
	}
	since, err := time.Parse(time.DateOnly, consent.UpdatesSince)
	if err != nil {
		t.Fatalf("UpdatesSince %q is not an ISO date: %v", consent.UpdatesSince, err)
	}
	last, err := time.Parse(time.DateOnly, lastAgreementWithoutUpdates)
	if err != nil {
		t.Fatalf("lastAgreementWithoutUpdates %q is not an ISO date: %v", lastAgreementWithoutUpdates, err)
	}
	if version.After(last) && !since.After(last) {
		t.Errorf("AgreementVersion %q is past %q, the last agreement without the Updates section, "+
			"but UpdatesSince %q is not: an acceptance of those terms would cover the automatic upgrade check",
			consent.AgreementVersion, lastAgreementWithoutUpdates, consent.UpdatesSince)
	}
}

func TestAgreement_VersionTextAndPrivacyChangeTogether(t *testing.T) {
	privacy, err := os.ReadFile(privacyMarkdownPath)
	if err != nil {
		t.Fatalf("read PRIVACY.md: %v", err)
	}

	got := [3]string{
		sha256Hex([]byte(consent.AgreementVersion)),
		sha256Hex([]byte(consent.AgreementText)),
		sha256Hex(privacy),
	}
	want := [3]string{
		pinnedAgreementVersionSHA256,
		pinnedAgreementTextSHA256,
		pinnedPrivacyMarkdownSHA256,
	}
	if got == want {
		return
	}

	names := [3]string{"AgreementVersion", "AgreementText", "PRIVACY.md"}
	var changed []string
	for i := range got {
		if got[i] != want[i] {
			changed = append(changed, names[i])
		}
	}
	t.Errorf("%s changed, but the pinned triple did not.\n"+
		"AgreementVersion, AgreementText, and PRIVACY.md must change together: "+
		"when you change any one of them, update the other two in the same change "+
		"and recompute the triple.\n"+
		"Current values:\n"+
		"\tpinnedAgreementVersionSHA256 = %q\n"+
		"\tpinnedAgreementTextSHA256    = %q\n"+
		"\tpinnedPrivacyMarkdownSHA256  = %q",
		strings.Join(changed, ", "), got[0], got[1], got[2])
}

func TestAgreementText_KeepsTheWordingsThatMustNotWeaken(t *testing.T) {
	text := consent.AgreementText
	for _, want := range []string{
		"the paths to their session files are\n   never constructed, so those files are never opened",
		"Tool results, however, are kept as\n   observed: their text may contain file paths from your machine",
		"the few fields whose value is, by construction, the\n   directory the session ran in are replaced with a placeholder",
		"Every other path a record holds is uploaded the\n   same way, as observed",
		"it cannot see\n   what an image or a PDF shows",
		"for a PNG, JPEG or GIF image, its\n   width and height",
		"Images that older versions of Claude Code keep\n   from a notebook's cell outputs are uploaded whatever this setting\n   says",
		"The branch name read from git passes the same\n   masking",
		"The request\n   carries the trajector version and nothing else",
		"To stop these\n   checks, set auto_upgrade to false in config.json",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("agreement text lost the wording %q", want)
		}
	}
	if strings.Contains(strings.ToLower(text), "hash") {
		t.Error("agreement text describes project paths as hashed; they are masked")
	}
	if strings.Contains(text, "identify where your project lives") {
		t.Error("agreement text claims every field that locates the project is masked; only the few that are the session's own directory are")
	}
	if strings.Contains(text, "as it is") {
		t.Error("agreement text says an image or a document is uploaded as it is; the copy a Read result keeps is still masked as text")
	}
	if strings.Contains(text, "we do not read") {
		t.Error("agreement text weakens 'never constructed' into a promise not to read")
	}
}

func TestAgreementText_DoesNotNameInternalTerms(t *testing.T) {
	lower := strings.ToLower(consent.AgreementText)
	for _, term := range []string{
		"transcript",
		"schema_version",
		"segment_index",
		"record_id",
		"record_kind",
		"session_id",
		"project_id_hash",
		"project_subpath",
		"meta_snapshot",
		"tooluseresult",
		"cwd",
		"gitbranch",
		"tail-only",
		"backfill",
	} {
		if strings.Contains(lower, term) {
			t.Errorf("agreement text names the internal term %q", term)
		}
	}
}
