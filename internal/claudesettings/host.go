package claudesettings

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Environment variables that move Claude Code's directories. They are
// read here and nowhere else, so every path this program builds for
// Claude Code is the one Claude Code itself reads.
const (
	// ConfigDirEnv is exported because a sentence about a file left in
	// the directory this variable moves has to name the variable, and
	// the name is spelled here.
	ConfigDirEnv       = "CLAUDE_CONFIG_DIR"
	envManagedSettings = "CLAUDE_CODE_MANAGED_SETTINGS_PATH"
)

// File and directory names Claude Code uses.
const (
	// deliveredSettingsName is the on-disk cache of the settings an
	// organization delivers through Claude Code's own service. It sits
	// in the configuration directory.
	deliveredSettingsName = "remote-settings.json"
	userSettingsName      = "settings.json"
	managedSettingsName   = "managed-settings.json"
	managedDropInDirName  = "managed-settings.d"
)

// Host is where Claude Code keeps its files on this machine. Both
// halves of that question are answered here — the per-user
// configuration directory, and the directory an organization's managed
// settings arrive in — so no other package builds a path of Claude
// Code's, and the variables that move either one are read in one
// place. A host is resolved from the machine by HostFor; a test builds
// one that points at its own tree.
type Host struct {
	// ConfigDir holds the user's own settings file, the cache of the
	// settings delivered to it, and the session files.
	ConfigDir string
	// ManagedDir holds managed-settings.json and the
	// managed-settings.d/ drop-in directory beside it. Empty means this
	// host has no managed settings to read.
	ManagedDir string
	// DefaultConfigDir is where the configuration directory is when
	// nothing moves it. It differs from ConfigDir only on a host that
	// names another directory, and it is carried beside ConfigDir
	// because a file trajector wrote into the default directory before
	// the move is now read by nobody: only a host holding both can say
	// that.
	DefaultConfigDir string
}

// HostFor resolves the Claude Code directories of the machine this
// program runs on. goos is the platform the binary was built for, and
// getenv reads the process environment.
func HostFor(goos, home string, getenv func(string) string) Host {
	return Host{
		ConfigDir:        configDir(home, getenv),
		ManagedDir:       managedSettingsDir(goos, getenv),
		DefaultConfigDir: defaultConfigDir(home),
	}
}

// configDir is Claude Code's configuration directory, which
// CLAUDE_CONFIG_DIR relocates whole.
func configDir(home string, getenv func(string) string) string {
	if dir := getenv(ConfigDirEnv); dir != "" {
		return dir
	}
	return defaultConfigDir(home)
}

// defaultConfigDir is the configuration directory of a host where
// nothing moves it. It is the well-known location, so it is also what
// a sentence about it spells as ~/.claude.
func defaultConfigDir(home string) string {
	return filepath.Join(home, ".claude")
}

// managedSettingsDir is where Claude Code reads the settings an
// organization manages for this host. The location is fixed per
// platform. CLAUDE_CODE_MANAGED_SETTINGS_PATH replaces the whole
// directory — it names a directory, not a file.
func managedSettingsDir(goos string, getenv func(string) string) string {
	if dir := getenv(envManagedSettings); dir != "" {
		return dir
	}
	switch goos {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

// UserSettingsPath locates the user-scoped settings file, which
// receives the discovery hook.
func (h Host) UserSettingsPath() string {
	return filepath.Join(h.ConfigDir, userSettingsName)
}

// UnreadUserSettingsPath locates a user settings file that Claude Code
// does not read: the one in the default configuration directory of a
// host whose configuration directory is elsewhere. It reports false
// where the two are the same directory, because there the file is the
// one Claude Code reads.
func (h Host) UnreadUserSettingsPath() (string, bool) {
	if h.DefaultConfigDir == "" || h.DefaultConfigDir == h.ConfigDir {
		return "", false
	}
	return filepath.Join(h.DefaultConfigDir, userSettingsName), true
}

// deliveredSettingsPath locates the cache of the settings an
// organization delivers through Claude Code's own service.
func (h Host) deliveredSettingsPath() string {
	return filepath.Join(h.ConfigDir, deliveredSettingsName)
}

// managedSettingsFiles lists the managed directory's files in the
// order Claude Code reads them: the base file, then its drop-ins. A
// host with no managed directory has none.
func (h Host) managedSettingsFiles() []string {
	if h.ManagedDir == "" {
		return nil
	}
	files := []string{filepath.Join(h.ManagedDir, managedSettingsName)}
	return append(files, dropInFiles(filepath.Join(h.ManagedDir, managedDropInDirName))...)
}

// Isolate points the Claude Code directories at a test's own tree,
// through the same variables a real machine is read with, so a test
// process never reads or writes the developer's own Claude Code files.
// set is the test's own setenv.
func Isolate(set func(key, value string), home string) {
	set(ConfigDirEnv, filepath.Join(home, "claude"))
	set(envManagedSettings, filepath.Join(home, "managed"))
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
