package consent_test

import (
	"os"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

func TestRepoTermsVersion_IsTheOnePrivacyNames(t *testing.T) {
	privacy, err := os.ReadFile(privacyMarkdownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(privacy), "The\ncurrent version of those terms is "+consent.RepoTermsVersion+".") {
		t.Errorf("PRIVACY.md does not name the repository assessment terms version %q", consent.RepoTermsVersion)
	}
}
