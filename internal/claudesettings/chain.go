package claudesettings

import "path/filepath"

// Source identifies where a configuration value was found.
type Source string

const (
	SourceProjectLocal Source = "project settings.local.json"
	SourceProject      Source = "project settings.json"
	SourceUser         Source = "user settings.json"
	SourceShell        Source = "shell environment"
)

// firstEnv resolves key the way Claude Code applies configuration:
// settings env blocks override the shell environment unconditionally,
// and narrower scopes win — project local, then project, then user
// settings, then the shell. A value accept refuses is skipped and the
// walk continues down the chain.
func firstEnv(projectRoot, home, key string, getenv func(string) string, accept func(string) bool) (string, Source, bool) {
	chain := []struct {
		path   string
		source Source
	}{
		{ProjectLocalPath(projectRoot), SourceProjectLocal},
		{projectSharedPath(projectRoot), SourceProject},
		{UserSettingsPath(home), SourceUser},
	}
	for _, link := range chain {
		if value, ok := fileEnv(link.path, key); ok && accept(value) {
			return value, link.source, true
		}
	}
	if value := getenv(key); value != "" && accept(value) {
		return value, SourceShell, true
	}
	return "", "", false
}

// EffectiveEnv resolves key to the value Claude Code would see.
func EffectiveEnv(projectRoot, home, key string, getenv func(string) string) (string, Source, bool) {
	return firstEnv(projectRoot, home, key, getenv, func(string) bool { return true })
}

// ExternalBaseURL resolves the base URL the project's traffic would use
// without trajector: the effective ANTHROPIC_BASE_URL with
// trajector-injected values skipped, so re-running enable sees through
// its own injection to the user's real configuration.
func ExternalBaseURL(projectRoot, home string, getenv func(string) string) (string, Source, bool) {
	return firstEnv(projectRoot, home, EnvBaseURL, getenv, func(value string) bool {
		return !IsProxyBaseURL(value)
	})
}

// UnsupportedChannel reports a Bedrock or Vertex configuration anywhere
// in the chain. Those channels use different routing and credentials;
// injecting a base URL there would break the user's setup, so enable
// must refuse.
func UnsupportedChannel(projectRoot, home string, getenv func(string) string) (string, bool) {
	for _, key := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		if value, _, ok := EffectiveEnv(projectRoot, home, key, getenv); ok && truthy(value) {
			return key, true
		}
	}
	return "", false
}

func truthy(value string) bool {
	return value != "" && value != "0" && value != "false"
}

func projectSharedPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".claude", "settings.json")
}

func fileEnv(path, key string) (string, bool) {
	root, err := readSettings(path)
	if err != nil {
		return "", false
	}
	env, ok := root["env"].(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := env[key].(string)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}
