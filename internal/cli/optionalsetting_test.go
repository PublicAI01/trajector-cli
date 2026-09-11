package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

func TestEnable_SecondRunKeepsAnOptionalSettingOnByDefault(t *testing.T) {
	for _, input := range []string{"y\n", "\n"} {
		t.Run(strings.TrimSuffix(input, "\n")+"<enter>", func(t *testing.T) {
			e := clitest.New(t)
			e.Paired()
			p := e.StartProxy()
			defer p.Stop()

			if got := e.InProjectInput("yes\ny\n", "enable"); got.Exit != 0 {
				t.Fatalf("enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
			}
			path := filepath.Join(e.Project(), ".claude", "settings.local.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			got := e.InProjectInput(input, "enable")
			if got.Exit != 0 {
				t.Fatalf("second enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
			}
			if !strings.Contains(got.Stdout, "Keep it on? [Y/n]") {
				t.Errorf("stdout misses the keep-it-on question:\n%s", got.Stdout)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Errorf("keeping the setting rewrote the file:\n%s\nwant:\n%s", after, before)
			}
			if !strings.Contains(string(after), `"showThinkingSummaries": true`) {
				t.Errorf("the setting is no longer on:\n%s", after)
			}
		})
	}
}

func TestEnable_SecondRunAnswerNoRestoresTheOptionalSetting(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	p := e.StartProxy()
	defer p.Stop()

	if got := e.InProjectInput("yes\ny\n", "enable"); got.Exit != 0 {
		t.Fatalf("enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
	}

	got := e.InProjectInput("n\n", "enable")
	if got.Exit != 0 {
		t.Fatalf("second enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Set showThinkingSummaries back to what it was before trajector wrote it.") {
		t.Errorf("stdout misses the undo line:\n%s", got.Stdout)
	}
	data, err := os.ReadFile(filepath.Join(e.Project(), ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "showThinkingSummaries") {
		t.Errorf("the key survived being turned back off:\n%s", data)
	}
}
