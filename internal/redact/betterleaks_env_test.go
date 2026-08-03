package redact

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// betterleaksEnvCheckChildVar marks the re-executed child half of
// TestBetterleaksDoesNotPoisonGitEnvironment.
const betterleaksEnvCheckChildVar = "REDACT_TEST_BETTERLEAKS_ENV_CHILD"

func TestBetterleaksDoesNotPoisonGitEnvironment(t *testing.T) {
	t.Parallel()

	// Regression coverage for betterleaks v1.1.1: importing the detector set
	// git isolation variables process-wide, and later git subprocesses could
	// no longer read user credential helpers from ~/.gitconfig. The check
	// re-executes this test binary so the detector initializes in a fresh
	// process whose environment carries none of the variables to begin with.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBetterleaksEnvCheckChild$")
	cmd.Env = append(envWithoutGitIsolation(), betterleaksEnvCheckChildVar+"=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("redact import polluted git environment: %v\n%s", err, output)
	}
}

// TestBetterleaksEnvCheckChild is the child half of
// TestBetterleaksDoesNotPoisonGitEnvironment; it only runs re-executed.
func TestBetterleaksEnvCheckChild(t *testing.T) {
	if os.Getenv(betterleaksEnvCheckChildVar) != "1" {
		t.Skip("child-process half of TestBetterleaksDoesNotPoisonGitEnvironment")
	}
	_ = String("key=AKIAYRWQG5EJLPZLBYNP")
	for _, name := range []string{
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_SYSTEM",
		"GIT_NO_REPLACE_OBJECTS",
		"GIT_TERMINAL_PROMPT",
	} {
		if value, ok := os.LookupEnv(name); ok {
			t.Errorf("%s=%s", name, value)
		}
	}
}

func envWithoutGitIsolation() []string {
	blocked := map[string]struct{}{
		"GIT_CONFIG_GLOBAL":      {},
		"GIT_CONFIG_NOSYSTEM":    {},
		"GIT_CONFIG_SYSTEM":      {},
		"GIT_NO_REPLACE_OBJECTS": {},
		"GIT_TERMINAL_PROMPT":    {},
	}

	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, ok := blocked[name]; ok {
			continue
		}
		env = append(env, entry)
	}
	return env
}
