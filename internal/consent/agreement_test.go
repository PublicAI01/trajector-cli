package consent_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

// The version, the agreement text, and PRIVACY.md state the same terms.
// The three hashes below pin them to each other: changing any one of the
// three without the other two fails the test.
const (
	pinnedAgreementVersionSHA256 = "cbdbacf80be93c3d1b7bef40ac4d8f54d0beeb1141f5565216f25df26baf9006"
	pinnedAgreementTextSHA256    = "13ed9eb0e2ce9d4b12503f36baaae2f95943cf9fa13939c63bffd214c76cab58"
	pinnedPrivacyMarkdownSHA256  = "4d07f49ed4af36d1f64f9d347aa574fb0606b840daa21cf33aae23d9cd71eb3c"
)

const privacyMarkdownPath = "../../PRIVACY.md"

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
