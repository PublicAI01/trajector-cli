package report

import (
	"net/url"
	"strings"
	"testing"
)

// The formatters are pure and feed user-facing surfaces; their edges
// (unit rollover, short tokens) are easier to pin here than through
// command output.

func TestIsWSLAnswersOnEveryPlatform(t *testing.T) {
	// The value is platform-truth (false on macOS and plain Linux, true
	// only under WSL); the invariant is that probing never panics and
	// never errors out of the doctor run.
	_ = isWSL()
}

func TestMaskTokenNeverRevealsShortTokens(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{"", ""},
		{"short", "masked"},
		{"12345678", "masked"},
		{"0123456789abcdef0123456789abcdef", "01234567…(masked)"},
	}
	for _, tt := range tests {
		if got := maskToken(tt.token); got != tt.want {
			t.Errorf("maskToken(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}
}

// TestMaskUpstreamCredentialsMasksAQueryThatWillNotParse pins the
// fail-open the masking had until 2026-08-22. It decided whether to mask
// by counting the pairs url.Values managed to parse, and Query drops any
// pair it cannot unescape without saying so — so a query whose pairs all
// fail parsed as no query at all, the masking loop never ran, and the
// relay credential went into the shared bundle verbatim.
func TestMaskUpstreamCredentialsMasksAQueryThatWillNotParse(t *testing.T) {
	// Each secret must not survive masking, whatever the query does to
	// url.ParseQuery: a bare '%' is an invalid escape, a ';' is rejected
	// outright, and a partly-parseable query must not leak the half that
	// failed either.
	secrets := []string{
		"https://relay.example.com/v1?token=Ab3;Xy9",
		"https://relay.example.com/v1?api_key=sk%ZZlive",
		"https://relay.example.com/v1?api_key=sk%ZZlive&region=eu",
		"https://relay.example.com/v1?api_key=sk-live-1234",
		"https://user:hunter2@relay.example.com/v1",
	}
	for _, raw := range secrets {
		got := maskUpstreamCredentials(raw)
		for _, secret := range []string{"Ab3;Xy9", "sk%ZZlive", "sk-live-1234", "hunter2"} {
			if strings.Contains(got, secret) {
				t.Errorf("maskUpstreamCredentials(%q) = %q, want %q gone", raw, got, secret)
			}
		}
		// The destination itself is the point of the diagnosis and stays.
		if !strings.Contains(got, "relay.example.com/v1") {
			t.Errorf("maskUpstreamCredentials(%q) = %q, want the host and path kept", raw, got)
		}
	}
	// A URL with nothing to strip is left exactly as it is.
	const plain = "https://relay.example.com/v1"
	if got := maskUpstreamCredentials(plain); got != plain {
		t.Errorf("maskUpstreamCredentials(%q) = %q, want it unchanged", plain, got)
	}
}

// TestMaskUpstreamCredentialsMasksAnUpstreamThatWillNotParse pins the
// fail-open one layer out from the query masking fixed on 2026-08-22:
// url.Parse refusing the whole value returned it unchanged, credentials
// and all. Go's parser is stricter than the one Claude Code uses, so
// "not a URL" here is routinely a working relay URL whose password
// holds a character url.Parse will not take — exactly the value that
// carries a secret. The bundle is the one artifact that leaves this
// machine, and the command hands it over saying it holds no credentials.
func TestMaskUpstreamCredentialsMasksAnUpstreamThatWillNotParse(t *testing.T) {
	const secret = "hun|ter2"
	unparseable := []string{
		"https://relay-user:" + secret + "@relay.example.com/v1",
		"https://relay-user:hun%ter2@relay.example.com/v1",
	}
	for _, raw := range unparseable {
		// The premise, asserted rather than assumed: if a future Go took
		// these, the case below would pass for the wrong reason.
		if _, err := url.Parse(raw); err == nil {
			t.Fatalf("test premise: url.Parse(%q) succeeded, pick a value it refuses", raw)
		}
		got := maskUpstreamCredentials(raw)
		for _, leaked := range []string{secret, "hun%ter2", "relay-user"} {
			if strings.Contains(got, leaked) {
				t.Errorf("maskUpstreamCredentials(%q) = %q, want %q gone", raw, got, leaked)
			}
		}
	}
	// An empty upstream is absence, not something to redact: a diagnosis
	// must still be able to show that nothing was recorded.
	if got := maskUpstreamCredentials(""); got != "" {
		t.Errorf("maskUpstreamCredentials(%q) = %q, want it empty", "", got)
	}
}
