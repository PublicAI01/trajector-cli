package consent_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

func TestRepoTerms_AcceptanceRoundTripsApartFromTheAgreement(t *testing.T) {
	s := consent.Open(filepath.Join(t.TempDir(), "consent.json"))
	if v, at, err := s.AcceptedRepoTerms(); err != nil || v != "" || at != "" {
		t.Fatalf("fresh store = %q, %q, %v; want no acceptance", v, at, err)
	}
	if err := s.AcceptAgreement(consent.AgreementVersion, "2026-11-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if v, _, err := s.AcceptedRepoTerms(); err != nil || v != "" {
		t.Fatalf("after the agreement = %q, %v; accepting the agreement must not accept the repository terms", v, err)
	}
	if err := s.AcceptRepoTerms(consent.RepoTermsVersion, "2026-11-16T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	v, at, err := s.AcceptedRepoTerms()
	if err != nil || v != consent.RepoTermsVersion || at != "2026-11-16T09:00:00Z" {
		t.Fatalf("AcceptedRepoTerms = %q, %q, %v", v, at, err)
	}
	if av, aat, err := s.AcceptedVersion(); err != nil || av != consent.AgreementVersion || aat != "2026-11-01T00:00:00Z" {
		t.Fatalf("AcceptedVersion = %q, %q, %v; the agreement record changed", av, aat, err)
	}
}

func TestRepoTerms_AcceptRejectsEmptyVersion(t *testing.T) {
	s := consent.Open(filepath.Join(t.TempDir(), "consent.json"))
	if err := s.AcceptRepoTerms("", "2026-11-16T09:00:00Z"); err == nil {
		t.Fatal("an empty version was recorded")
	}
}

func TestRepoTermsVersion_IsADaySaidInTheText(t *testing.T) {
	if _, err := time.Parse(time.DateOnly, consent.RepoTermsVersion); err != nil {
		t.Fatalf("RepoTermsVersion %q is not a day (YYYY-MM-DD): %v", consent.RepoTermsVersion, err)
	}
	if !strings.Contains(consent.RepoTermsText, "("+consent.RepoTermsVersion+")") {
		t.Errorf("the terms do not say the version %q they are shown under", consent.RepoTermsVersion)
	}
	if consent.RepoTermsVersion == consent.AgreementVersion && consent.RepoTermsText == consent.AgreementText {
		t.Error("the repository terms are the agreement")
	}
}

func TestRepoTermsText_KeepsWhatTheUserMustBeTold(t *testing.T) {
	text := strings.Join(strings.Fields(consent.RepoTermsText), " ")
	for _, wording := range []string{
		"the commit message without the lines that name a person (such as Co-authored-by and Signed-off-by), and the changed file paths",
		"Any other name, path, URL or word written in a commit message is sent as it was written",
		"No file content and no diff",
		"No author name or email address of any commit, and no message line that names a person",
		"No remote URL and no host name of a remote",
		"No location of the repository on your machine",
		"Trajector uses a submission to assess the repository and to decide whether to contact you",
		"Trajector may also use submissions in aggregate, in a form that identifies no repository and no person, to check and adjust how it assesses repositories.",
		"It uses a submission for nothing beyond these two uses.",
		"A submission gives no one any right to your code",
		"It does not commit you to sell or license anything",
		"it does not commit trajector to contact you",
		"support@publicai.io",
		"If you ask us to close your trajector account, we delete all of your submissions.",
		"you confirm that you have the right to share this information",
		"Nothing is sent before you confirm",
		"The preview file shows everything that is sent",
	} {
		if !strings.Contains(text, wording) {
			t.Errorf("the repository terms lost the wording %q", wording)
		}
	}
	for _, overclaim := range []string{"no names", "no paths on your machine", "Deleting your trajector account"} {
		if strings.Contains(text, overclaim) {
			t.Errorf("the repository terms say %q, which the client does not keep to", overclaim)
		}
	}
}

func TestRepoTermsText_ImpliesNoStepOnTheServiceSide(t *testing.T) {
	words := strings.FieldsFunc(strings.ToLower(consent.RepoTermsText), func(r rune) bool {
		return (r < 'a' || r > 'z') && r != '-'
	})
	for _, w := range words {
		for _, stem := range []string{"review", "approv", "accepted", "tier", "qualif", "eligib", "score", "rank"} {
			if strings.HasPrefix(w, stem) {
				t.Errorf("the repository terms say %q", w)
			}
		}
	}
}
