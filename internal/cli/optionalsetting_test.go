package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
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
			settings := e.ProjectSettings()
			before := settings.Contents()

			got := e.InProjectInput(input, "enable")
			if got.Exit != 0 {
				t.Fatalf("second enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
			}
			if !strings.Contains(got.Stdout, "Keep it on? [Y/n]") {
				t.Errorf("stdout misses the keep-it-on question:\n%s", got.Stdout)
			}
			after := settings.Contents()
			if after != before {
				t.Errorf("keeping the setting rewrote the file:\n%s\nwant:\n%s", after, before)
			}
			if value, found := settings.TopLevelBool(proxytest.KeyShowThinkingSummaries); !found || !value {
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
	settings := e.ProjectSettings()
	if _, found := settings.TopLevelBool(proxytest.KeyShowThinkingSummaries); found {
		t.Errorf("the key survived being turned back off:\n%s", settings.Contents())
	}
}
