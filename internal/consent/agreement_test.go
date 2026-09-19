package consent_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

// agreementVersionFloor is the version the terms before these carried.
// A version that does not pass it leaves every earlier acceptance
// current, so nobody is asked to reconfirm and the pause never fires.
const agreementVersionFloor = "2026-08-31"

// The version, the agreement text, and PRIVACY.md state the same terms.
// The three hashes below pin them to each other: changing any one of the
// three without the other two fails the test.
const (
	pinnedAgreementVersionSHA256 = "cbdbacf80be93c3d1b7bef40ac4d8f54d0beeb1141f5565216f25df26baf9006"
	pinnedAgreementTextSHA256    = "c784f08346c8b7242d6151c15480c8e9dd64782dd7df804d1974c4075ca1be84"
	pinnedPrivacyMarkdownSHA256  = "72d09687dcb83954d6fe76871cd12a4ad90ee093964023411c233bccb65d2799"
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
