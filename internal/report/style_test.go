package report

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// env turns a table's environment into the lookup DetectStyle reads. A
// key present with an empty value is still present: NO_COLOR is honoured
// however it is spelled.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := pairs[key]
		return v, ok
	}
}

func TestDetectStyleReadsTheTerminalAndTheEnvironment(t *testing.T) {
	terminal, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	// The premise of every terminal case: this file is a character
	// device, which is what the detection reads.
	if !isTerminal(terminal) {
		t.Skip("this platform's null device is not a character device")
	}

	tests := []struct {
		name            string
		toATerminal     bool
		environment     map[string]string
		wantGlyph       string
		wantColorOnText bool
	}{
		{
			name:        "a pipe gets no glyph and no colour",
			environment: map[string]string{"LANG": "en_US.UTF-8", "TERM": "xterm-256color"},
			wantGlyph:   "",
		},
		{
			name:            "a UTF-8 terminal gets the drawn glyphs and colour",
			toATerminal:     true,
			environment:     map[string]string{"LANG": "en_US.UTF-8", "TERM": "xterm-256color"},
			wantGlyph:       "✗",
			wantColorOnText: true,
		},
		{
			name:            "a terminal in another encoding gets the ASCII glyphs",
			toATerminal:     true,
			environment:     map[string]string{"LANG": "en_US.ISO-8859-1", "TERM": "xterm"},
			wantGlyph:       "x",
			wantColorOnText: true,
		},
		{
			name:            "a terminal with no locale set gets the ASCII glyphs",
			toATerminal:     true,
			environment:     map[string]string{"TERM": "xterm"},
			wantGlyph:       "x",
			wantColorOnText: true,
		},
		{
			name:        "LC_ALL decides over LANG",
			toATerminal: true,
			environment: map[string]string{
				"LC_ALL": "C", "LC_CTYPE": "en_US.UTF-8", "LANG": "en_US.UTF-8", "TERM": "xterm",
			},
			wantGlyph:       "x",
			wantColorOnText: true,
		},
		{
			name:            "LC_CTYPE decides over LANG",
			toATerminal:     true,
			environment:     map[string]string{"LC_CTYPE": "en_US.utf8", "LANG": "C", "TERM": "xterm"},
			wantGlyph:       "✓",
			wantColorOnText: true,
		},
		{
			name:            "NO_COLOR set to anything keeps the glyph and drops the colour",
			toATerminal:     true,
			environment:     map[string]string{"NO_COLOR": "", "LANG": "en_US.UTF-8", "TERM": "xterm"},
			wantGlyph:       "✗",
			wantColorOnText: false,
		},
		{
			name:            "a dumb terminal keeps the glyph and drops the colour",
			toATerminal:     true,
			environment:     map[string]string{"TERM": "dumb", "LANG": "en_US.UTF-8"},
			wantGlyph:       "✗",
			wantColorOnText: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w io.Writer = new(bytes.Buffer)
			if tt.toATerminal {
				w = terminal
			}
			style := DetectStyle(w, env(tt.environment))
			sev := severityError
			if strings.Contains(tt.name, "LC_CTYPE") {
				sev = severityOK
			}
			if got := style.glyph(sev); got != tt.wantGlyph {
				t.Errorf("glyph = %q, want %q", got, tt.wantGlyph)
			}
			if got := strings.Contains(style.marker(severityError), "\x1b["); got != tt.wantColorOnText {
				t.Errorf("coloured = %v, want %v", got, tt.wantColorOnText)
			}
		})
	}
}

func TestPlainStyleWritesNothingATerminalWouldInterpret(t *testing.T) {
	var style Style
	for _, sev := range []severity{severityOK, severityFixed, severityNote, severityWarning, severityError} {
		marker := style.marker(sev)
		for _, b := range []byte(marker) {
			if b > 0x7e || b < 0x20 {
				t.Fatalf("marker for %s = %q, want pure printable ASCII", sev.label(), marker)
			}
		}
	}
	if got := style.marker(severityError); got != "error: " {
		t.Errorf("marker = %q, want %q", got, "error: ")
	}
	if got := style.marker(severityWarning); got != "warning: " {
		t.Errorf("marker = %q, want %q", got, "warning: ")
	}
}

func TestNoColorLeavesTheGlyphsInPlace(t *testing.T) {
	coloured := Style{tty: true, unicode: true, color: true}
	plainer := coloured.WithoutColor()
	if strings.Contains(plainer.marker(severityError), "\x1b[") {
		t.Errorf("marker = %q, want no escape sequence", plainer.marker(severityError))
	}
	if !strings.Contains(plainer.marker(severityError), "✗") {
		t.Errorf("marker = %q, want the glyph kept", plainer.marker(severityError))
	}
}
