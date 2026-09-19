package proxytest

import (
	"runtime"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// Settings is one settings file Claude Code reads on this device.
// Tests drive it through the operations the machine itself performs,
// so nothing above the machine has to name the file's shape.
type Settings struct {
	t    *testing.T
	path string
}

// settingsAt wraps the settings file at path.
func settingsAt(t *testing.T, path string) *Settings { return &Settings{t: t, path: path} }

// ProjectSettings wraps the settings file a project is enabled in.
// projectRoot is the project directory in the form every derivation of
// a project identity uses.
func ProjectSettings(t *testing.T, projectRoot string) *Settings {
	return settingsAt(t, claudesettings.ProjectLocalPath(projectRoot))
}

const (
	// ProjectLocalRel is where the file sits inside a project, and
	// ProjectLocalIgnoreRule the rule that keeps it and its neighbors
	// out of a repository.
	ProjectLocalRel        = claudesettings.ProjectLocalRel
	ProjectLocalIgnoreRule = claudesettings.ProjectLocalIgnoreRule
)

// Source is a rank of the settings chain, in claudesettings' own type,
// with the ranks a test names. Tests name them through the harness so
// nothing above the machine has to reach into the settings chain's
// vocabulary.
type Source = claudesettings.Source

const (
	SourceUser         = claudesettings.SourceUser
	SourceProjectLocal = claudesettings.SourceProjectLocal
)

// The subcommands naming the hooks a test speaks about one by one.
const (
	HookEnsureProxy = claudesettings.HookEnsureProxy
	HookSessionEnd  = claudesettings.HookSessionEnd
)

// The markers that tell each hook trajector installs from every other
// command in the file.
const (
	EnsureProxyMarker = claudesettings.EnsureProxyMarker
	SessionEndMarker  = claudesettings.SessionEndMarker
	GitSnapshotMarker = claudesettings.GitSnapshotMarker
	ProgressMarker    = claudesettings.ProgressMarker
	DiscoveryMarker   = claudesettings.DiscoveryMarker
)

// KeyShowThinkingSummaries is the optional setting trajector asks
// about, named from the one module that states it.
const KeyShowThinkingSummaries = claudesettings.KeyShowThinkingSummaries

// ConfigDirEnv points Claude Code's configuration directory somewhere
// other than the default.
const ConfigDirEnv = claudesettings.ConfigDirEnv

// HookCommands is the command installed for each session hook, in
// claudesettings' own type.
type HookCommands = claudesettings.HookCommands

// ProjectHooks spells the hook commands an enable installs for the
// binary at execPath: one for every hook this release installs, so a
// test drives the same list the machine does.
func ProjectHooks(execPath string) HookCommands {
	hooks := HookCommands{}
	for _, subcommand := range claudesettings.ProjectHookSubcommands() {
		hooks[subcommand] = execPath + " hook " + subcommand
	}
	return hooks
}

// Path is where this settings file lives.
func (s *Settings) Path() string { return s.path }

// Contents is the file verbatim.
func (s *Settings) Contents() string {
	s.t.Helper()
	data, err := fsatomic.ReadFile(s.path)
	if err != nil {
		s.t.Fatal(err)
	}
	return string(data)
}

// Put replaces the file with content verbatim, so a test can plant
// what no injection of ours would produce.
func (s *Settings) Put(content string) {
	s.t.Helper()
	writeFile(s.t, s.path, content)
}

// HasHook reports whether a hook carrying marker stands in the file.
func (s *Settings) HasHook(marker string) bool {
	return claudesettings.HasHook(s.path, marker)
}

// Inject writes what an enable on the device whose binary is at
// execPath writes: with a base URL, the shape that routes the
// project's traffic through the proxy; with an empty one, the shape
// that leaves the traffic alone.
func (s *Settings) Inject(execPath, baseURL string) {
	s.t.Helper()
	if err := claudesettings.InjectProject(s.path, baseURL, ProjectHooks(execPath)); err != nil {
		s.t.Fatal(err)
	}
}

// RemoveInjection takes the injected base URL and every session hook
// back out, as a disable would.
func (s *Settings) RemoveInjection() {
	s.t.Helper()
	if err := claudesettings.RemoveProject(s.path); err != nil {
		s.t.Fatal(err)
	}
}

// InjectedBaseURL reports the base URL an injection of ours stands in
// the file, and false when none does.
func (s *Settings) InjectedBaseURL() (string, bool) {
	return claudesettings.InjectedBaseURL(s.path)
}

// Shape reports which shape of injection stands in the file, and false
// when nothing of ours does. It is a reading of the file, never what
// the project records in: the grant holds that.
func (s *Settings) Shape() (Shape, bool) {
	return claudesettings.InjectionShape(s.path)
}

// TopLevelBool reads a top-level boolean key. found separates an
// explicit false from an absent key.
func (s *Settings) TopLevelBool(key string) (value, found bool) {
	return claudesettings.TopLevelBool(s.path, key)
}

// SetTopLevelBool writes value under a top-level key, as a hand edit
// would, leaving the rest of the file alone.
func (s *Settings) SetTopLevelBool(key string, value bool) {
	s.t.Helper()
	if err := claudesettings.SetTopLevelBool(s.path, key, value); err != nil {
		s.t.Fatal(err)
	}
}

// Claude locates Claude Code's configuration on a device the way the
// code under test locates it.
type Claude struct {
	t      *testing.T
	host   claudesettings.Host
	getenv func(string) string
}

// ClaudeConfig reads the configuration of a device whose home is home
// and whose environment getenv answers.
func ClaudeConfig(t *testing.T, home string, getenv func(string) string) *Claude {
	return &Claude{t: t, host: claudesettings.HostFor(runtime.GOOS, home, getenv), getenv: getenv}
}

// UserSettings is the user-scoped settings file Claude Code reads on
// this device.
func (c *Claude) UserSettings() *Settings {
	return settingsAt(c.t, c.host.UserSettingsPath())
}

// DefaultUserSettings is the user-scoped settings file of the default
// configuration directory: the file a device that points the directory
// elsewhere leaves behind, and the one Claude Code reads where nothing
// moves it.
func (c *Claude) DefaultUserSettings() *Settings {
	if path, elsewhere := c.host.UnreadUserSettingsPath(); elsewhere {
		return settingsAt(c.t, path)
	}
	return c.UserSettings()
}

// ExternalBaseURL is the base URL a project's configuration chain
// names once an injection of ours is out of the way.
func (c *Claude) ExternalBaseURL(projectRoot string) string {
	value, _, _ := claudesettings.ExternalBaseURL(projectRoot, c.host, c.getenv)
	return value
}

// TokenFromBaseURL reads the token an injected base URL routes at, in
// claudesettings' own reading, so a test can say which grant an
// injection names without spelling the URL's shape.
func TokenFromBaseURL(baseURL string) (string, bool) { return claudesettings.TokenFromBaseURL(baseURL) }
