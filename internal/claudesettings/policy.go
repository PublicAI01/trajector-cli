package claudesettings

import "strings"

// HookPolicy is whether Claude Code, judged from the configuration
// readable on this host, will run the hooks trajector injects into a
// project's local settings.
//
// Only readable evidence counts. Claude Code takes the same switches
// from places a static reading cannot see: the --safe-mode, --bare,
// --settings and --managed-settings command-line arguments; an
// environment set for the Claude Code process alone; settings an
// organization pushes through macOS managed preferences or the
// Windows registry; Windows-side managed settings a WSL session
// inherits; and the workspace-trust dialog. A missing or malformed
// file reads as saying nothing, so a broken file never produces a
// "will not run" answer on its own.
type HookPolicy struct {
	Runs bool
	// Reason names the setting that decided against running and the
	// Source that set it, empty when Runs is true. It names the rank,
	// not the file: the user acts on "the managed settings", and where
	// a rank is spelled on disk can differ per machine.
	Reason string
}

// Settings that keep hooks from user, project, and local settings from
// loading. Claude Code honors them from managed settings only.
const (
	keyDisableAllHooks               = "disableAllHooks"
	keyAllowManagedHooksOnly         = "allowManagedHooksOnly"
	keyStrictPluginOnlyCustomization = "strictPluginOnlyCustomization"
)

var managedLockKeys = []string{keyDisableAllHooks, keyAllowManagedHooksOnly, keyStrictPluginOnlyCustomization}

// processModeKeys are the environment variables that start Claude Code
// in a mode where hooks from user, project, and local settings are not
// loaded: safe mode keeps managed hooks alone, bare mode runs none,
// restricted mode drops those three settings scopes entirely. Claude
// Code takes each from the shell, from the user settings env block, or
// from managed settings; a project or local env block cannot set them.
var processModeKeys = []string{"CLAUDE_CODE_SAFE_MODE", "CLAUDE_CODE_SIMPLE", "CLAUDE_CODE_RESTRICTED"}

// JudgeHookPolicy decides HookPolicy from what is on disk and in the
// environment right now. Nothing is cached: every call reads the files
// again, so the answer never describes an earlier state of the host.
//
// Three questions are asked over the one settings chain, each of the
// ranks that may answer it. A managed source that locks hooks decides
// regardless of what the sources above it say: Claude Code itself may
// let the highest source present stand alone, but whether the cached
// delivered settings are the ones in force cannot be told from disk,
// so a readable lock is reported as a lock rather than assumed to be
// shadowed. The mode keys and the plain disableAllHooks are the other
// way round — the highest rank that sets one decides it, whichever way
// it is set.
func JudgeHookPolicy(projectRoot string, host Host, getenv func(string) string) HookPolicy {
	if d, found := host.chain(projectRoot, getenv, layerManaged).
		decide(managedLockKeys, topLevelValue, locksHooks); found {
		return decidedAgainst(d)
	}

	modes := host.chain(projectRoot, getenv, layerManaged|layerUser|layerShell)
	for _, key := range processModeKeys {
		if d, found := modes.decide([]string{key}, envEntry, anyValue); found && modeSwitchOn(d.value.(string)) {
			return decidedAgainst(d)
		}
	}

	if d, found := host.chain(projectRoot, getenv, layerProject|layerUser).
		decide([]string{keyDisableAllHooks}, topLevelBoolValue, anyValue); found && d.value.(bool) {
		return decidedAgainst(d)
	}
	return HookPolicy{Runs: true}
}

func decidedAgainst(d decision) HookPolicy {
	return HookPolicy{Reason: d.key + " in " + string(d.source)}
}

// locksHooks reports whether value, read under key from a managed
// source, keeps non-managed hooks from loading. The strict
// customization lock is either a bare true or a list of the areas it
// covers.
func locksHooks(key string, value any) bool {
	if value == true {
		return true
	}
	if key != keyStrictPluginOnlyCustomization {
		return false
	}
	areas, ok := value.([]any)
	if !ok {
		return false
	}
	for _, area := range areas {
		if area == "hooks" {
			return true
		}
	}
	return false
}

// modeSwitchOn parses a mode variable the way Claude Code does. This
// is narrower than truthy: only these four spellings switch a mode on,
// so "2" or "enabled" do not.
func modeSwitchOn(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
