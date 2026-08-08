package lifecycle

import "testing"

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
