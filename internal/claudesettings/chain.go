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

// effectiveEnv resolves key to the value Claude Code would see.
func effectiveEnv(projectRoot, home, key string, getenv func(string) string) (string, Source, bool) {
	return firstEnv(projectRoot, home, key, getenv, func(string) bool { return true })
}

// BaseURLResolution says how much the configuration chain could tell us
// about where a project's traffic would go without trajector.
type BaseURLResolution int

const (
	// BaseURLNone: nothing in the chain sets a base URL, so the
	// official endpoint is what the project would use.
	BaseURLNone BaseURLResolution = iota
	// BaseURLExternal: the user configured a base URL of their own.
	BaseURLExternal
	// BaseURLMasked: the shell environment carries trajector's own
	// injection, applied by a Claude Code process that already read the
	// settings. Whatever the user's shell configured is hidden behind
	// it, so the answer is "unknown" — never "the official endpoint".
	BaseURLMasked
)

// ExternalBaseURL resolves the base URL the project's traffic would use
// without trajector: the effective ANTHROPIC_BASE_URL with
// trajector-injected values skipped, so re-running enable sees through
// its own injection to the user's real configuration.
//
// Skipping our injection in the settings files is always right — we
// wrote those. Meeting it in the shell environment is a different
// situation: a Claude Code process applied the settings env block to
// everything it spawns, so our own value has replaced the one the
// user's shell exported. That reads as BaseURLMasked, because treating
// it as "nothing configured" would send a relay user's traffic — and
// their relay credentials — to the official endpoint.
func ExternalBaseURL(projectRoot, home string, getenv func(string) string) (string, Source, BaseURLResolution) {
	value, source, found := firstEnv(projectRoot, home, envBaseURL, getenv, func(value string) bool {
		return !isProxyBaseURL(value)
	})
	if found {
		return value, source, BaseURLExternal
	}
	if isProxyBaseURL(getenv(envBaseURL)) {
		return "", SourceShell, BaseURLMasked
	}
	return "", "", BaseURLNone
}

// UnsupportedChannel reports a Bedrock or Vertex configuration anywhere
// in the chain. Those channels use different routing and credentials;
// injecting a base URL there would break the user's setup, so enable
// must refuse.
func UnsupportedChannel(projectRoot, home string, getenv func(string) string) (string, bool) {
	for _, key := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		if value, _, ok := effectiveEnv(projectRoot, home, key, getenv); ok && truthy(value) {
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
	return envValue(root, key)
}
