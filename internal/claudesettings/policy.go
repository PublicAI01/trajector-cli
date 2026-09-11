package claudesettings

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

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
	// Reason names the setting and the file that decided against
	// running, empty when Runs is true.
	Reason string
}

// ManagedDirs locates the settings an organization manages for this
// host. userdirs resolves it for the running platform; tests point it
// at a temporary directory.
type ManagedDirs struct {
	// Policy holds managed-settings.json and the managed-settings.d/
	// drop-in directory beside it. Empty means there is none to read.
	Policy string
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

// File names of the managed settings sources.
const (
	// remoteSettingsName is the on-disk cache of settings the
	// organization delivers through Claude Code's own service. It sits
	// beside the user settings file.
	remoteSettingsName   = "remote-settings.json"
	managedSettingsName  = "managed-settings.json"
	managedDropInDirName = "managed-settings.d"
)

// JudgeHookPolicy decides HookPolicy from what is on disk and in the
// environment right now. Nothing is cached: every call reads the files
// again, so the answer never describes an earlier state of the host.
//
// The managed sources are checked in the order Claude Code ranks them
// — the delivered-settings cache, then the managed directory — and a
// source that locks hooks decides regardless of what the sources above
// it say. Claude Code itself may let the highest source present stand
// alone, but whether the cached delivered settings are the ones in
// force cannot be told from disk, so a readable lock is reported as a
// lock rather than assumed to be shadowed.
func JudgeHookPolicy(projectRoot, home string, getenv func(string) string, managed ManagedDirs) HookPolicy {
	tiers := managedTiers(home, managed)
	for _, tier := range tiers {
		for _, key := range managedLockKeys {
			if value, path, found := tier.lookup(key); found && locksHooks(key, value) {
				return decidedAgainst(key, path)
			}
		}
	}

	envTiers := append(tiers, managedTier{readManagedFile(UserSettingsPath(home))})
	for _, key := range processModeKeys {
		value, where, found := lookupEnvAcross(envTiers, key)
		if !found {
			value, where, found = getenv(key), string(SourceShell), getenv(key) != ""
		}
		if found && modeSwitchOn(value) {
			return decidedAgainst(key, where)
		}
	}

	if value, source, found := firstTopLevelBool(projectRoot, home, keyDisableAllHooks); found && value {
		return decidedAgainst(keyDisableAllHooks, scopePath(projectRoot, home, source))
	}
	return HookPolicy{Runs: true}
}

func decidedAgainst(key, where string) HookPolicy {
	return HookPolicy{Runs: false, Reason: key + " in " + where}
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

func scopePath(projectRoot, home string, source Source) string {
	switch source {
	case SourceProjectLocal:
		return ProjectLocalPath(projectRoot)
	case SourceProject:
		return projectSharedPath(projectRoot)
	default:
		return UserSettingsPath(home)
	}
}

// managedFile is one settings file of a managed source. A file that is
// missing or cannot be parsed has a nil root and answers no lookup.
type managedFile struct {
	path string
	root map[string]any
}

func readManagedFile(path string) managedFile {
	root, err := readSettings(path)
	if err != nil {
		return managedFile{path: path}
	}
	return managedFile{path: path, root: root}
}

// managedTier is the files of one managed source in reading order. A
// later file overrides an earlier one key by key, so the last file that
// sets a key is the one that decides it.
type managedTier []managedFile

func (t managedTier) lookup(key string) (value any, path string, found bool) {
	for i := len(t) - 1; i >= 0; i-- {
		if value, found = t[i].root[key]; found {
			return value, t[i].path, true
		}
	}
	return nil, "", false
}

func (t managedTier) lookupEnv(key string) (value, path string, found bool) {
	for i := len(t) - 1; i >= 0; i-- {
		if value, found = envValue(t[i].root, key); found {
			return value, t[i].path, true
		}
	}
	return "", "", false
}

// lookupEnvAcross resolves an env key over tiers ranked highest first.
func lookupEnvAcross(tiers []managedTier, key string) (value, path string, found bool) {
	for _, tier := range tiers {
		if value, path, found = tier.lookupEnv(key); found {
			return value, path, true
		}
	}
	return "", "", false
}

// managedTiers reads the managed sources highest-ranked first: the
// delivered-settings cache, then the managed directory's base file
// followed by its drop-ins in file-name order.
func managedTiers(home string, managed ManagedDirs) []managedTier {
	tiers := []managedTier{{readManagedFile(remoteSettingsPath(home))}}
	if managed.Policy == "" {
		return tiers
	}
	files := managedTier{readManagedFile(filepath.Join(managed.Policy, managedSettingsName))}
	for _, path := range dropInFiles(filepath.Join(managed.Policy, managedDropInDirName)) {
		files = append(files, readManagedFile(path))
	}
	return append(tiers, files)
}

func remoteSettingsPath(home string) string {
	return filepath.Join(home, ".claude", remoteSettingsName)
}

// dropInFiles lists the drop-in files Claude Code reads from dir, in
// the order it reads them: regular files or symbolic links named
// *.json, dot-files excluded, sorted by name. An unreadable directory
// contributes nothing.
func dropInFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		if kind := entry.Type(); !kind.IsRegular() && kind&fs.ModeSymlink == 0 {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	return files
}
