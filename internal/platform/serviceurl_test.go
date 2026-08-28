package platform_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/platform"
)

func TestSafeServiceURLKeepsAddressesAUserCanOpen(t *testing.T) {
	for _, s := range []string{
		"https://dashboard.example.com/authorization",
		"https://dashboard.example.com/authorization?from=cli",
		"https://dashboard.example.com:8443/a/b/c#section",
	} {
		if got := platform.SafeServiceURL(s); got != s {
			t.Errorf("SafeServiceURL(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestSafeServiceURLNeverTruncates(t *testing.T) {
	// A cut URL is a link that opens nothing, and the user cannot tell
	// that is what happened. Too long is discarded whole so the caller
	// falls back to wording of its own.
	long := "https://dashboard.example.com/" + strings.Repeat("p", 1000)
	if got := platform.SafeServiceURL(long); got != "" {
		t.Fatalf("SafeServiceURL of an over-long address = %q, want it discarded whole", got)
	}
}

func TestSafeServiceURLRefusesWhatIsNotAnAbsoluteHTTPSAddress(t *testing.T) {
	for _, s := range []string{
		"",
		"/authorization",
		"authorization",
		"http://dashboard.example.com/authorization",
		"ftp://dashboard.example.com/authorization",
		"javascript:alert(1)",
		"https:///authorization",
		"mailto:support@example.com",
		"://dashboard.example.com",
	} {
		if got := platform.SafeServiceURL(s); got != "" {
			t.Errorf("SafeServiceURL(%q) = %q, want empty", s, got)
		}
	}
}

func TestSafeServiceURLRefusesAnAddressThatReadsAsOneHostAndResolvesToAnother(t *testing.T) {
	if got := platform.SafeServiceURL("https://dashboard.example.com@attacker.example/authorization"); got != "" {
		t.Fatalf("SafeServiceURL = %q, want userinfo refused", got)
	}
}

func TestSafeServiceURLRefusesRunesThatLetAPrintedLineLieAboutItself(t *testing.T) {
	for _, s := range []string{
		"https://dashboard.example.com/a\x1b[31mb",
		"https://dashboard.example.com/a\nStatus: OK",
		"https://dashboard.example.com/a b",
		"https://dashboard.example.com/‮evil",
		"https://dashboard.example.com/a​b",
		"https://dashboard.example.com/\xff\xfe",
	} {
		if got := platform.SafeServiceURL(s); got != "" {
			t.Errorf("SafeServiceURL(%q) = %q, want empty", s, got)
		}
	}
}
