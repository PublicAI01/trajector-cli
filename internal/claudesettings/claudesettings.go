// Package claudesettings edits and reads Claude Code settings files on
// trajector's behalf. All edits are merges: the user's own settings
// content is preserved byte-for-byte at the JSON level, and removal
// deletes exactly what trajector injected, nothing else.
package claudesettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// envBaseURL is the environment key Claude Code reads for its API base
// URL; injecting it is how a project's traffic is routed through the
// local proxy.
const envBaseURL = "ANTHROPIC_BASE_URL"

// Hook events used for injection.
const (
	eventSessionStart     = "SessionStart"
	eventUserPromptSubmit = "UserPromptSubmit"
	eventSessionEnd       = "SessionEnd"
)

// Hook subcommands of the trajector command line. The word is what a
// settings file carries, what the command line dispatches on, and what
// its usage text lists, so it is spelled here once.
const (
	HookEnsureProxy = "ensure-proxy"
	HookSessionEnd  = "session-end"
	HookDiscovery   = "discovery"
	HookRead        = "read"
)

// Marker substrings identifying trajector-injected hook commands, so
// removal never touches a hook the user wrote themselves.
const (
	EnsureProxyMarker = "hook " + HookEnsureProxy
	SessionEndMarker  = "hook " + HookSessionEnd
	DiscoveryMarker   = "hook " + HookDiscovery
)

// NoProxyMarker is the argument the ensure-proxy hook command carries
// when the injection routes nothing through the proxy. It is spelled
// like the enable flag that asks for that form, so the settings file
// states the user's choice in the user's own words, and it rides on
// the hook command so it is installed, recognized, and removed by the
// same walk as the hook itself.
const NoProxyMarker = "--no-proxy"

// projectMarkers are the markers of every hook a project injection
// installs; removal and re-injection treat a hook carrying any of them
// as trajector's own.
var projectMarkers = []string{EnsureProxyMarker, SessionEndMarker}

// HookCommands are the shell commands a project injection installs:
// EnsureProxy under SessionStart and UserPromptSubmit, SessionEnd under
// SessionEnd.
type HookCommands struct {
	EnsureProxy string
	SessionEnd  string
}

// errBaseURLInjected reports an attempt to inject without a base URL
// into a file that still carries an injected one.
var errBaseURLInjected = errors.New("claudesettings: an injected base URL stands in the settings file")

// ProjectLocalRel is the injected settings file's path relative to the
// project root, in slash form for gitignore entries and user-facing
// messages. Every spelling of the name derives from this one.
const ProjectLocalRel = ".claude/settings.local.json"

// ProjectLocalIgnoreRule is the gitignore pattern enable maintains for
// the injected settings file. It is deliberately wider than the file
// itself: an atomic rewrite that dies midway leaves a temp or lock
// sibling that can carry the same consent token, and none of those may
// be committable either.
const ProjectLocalIgnoreRule = ProjectLocalRel + "*"

// ProjectLocalPath locates the project-scoped settings file that
// receives the injection. It is local (never committed) by Claude Code
// convention; enable additionally verifies gitignore coverage.
func ProjectLocalPath(projectRoot string) string {
	return filepath.Join(projectRoot, filepath.FromSlash(ProjectLocalRel))
}

// UserSettingsPath locates the user-scoped settings file that receives
// the discovery hook.
func UserSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// proxyBaseURL recognizes a base URL injected by trajector: loopback
// host with a /t/<token> path. Matching stays narrow so removal can
// never mistake a user's own relay URL for our injection.
//
// The host alternatives must cover every address apiproxy.ValidateAddr
// lets the proxy bind, because injection spells this URL from whatever
// address the proxy is serving. Recognizing 127.0.0.1 alone left an
// injection on any other address in the 127/8 block unrecognized — so
// disable and uninstall walked past it, and the user's own sessions
// kept pointing at a port that would soon be dead, with no command able
// to clear it. The whole block is matched here for that reason; it is
// still narrower than "any host". 2026-08-15.
var proxyBaseURL = regexp.MustCompile(`^http://(127(?:\.[0-9]{1,3}){3}|localhost|\[::1\]):[0-9]+/t/([A-Za-z0-9._-]+)$`)

// isProxyBaseURL reports whether value is a trajector-injected base
// URL.
func isProxyBaseURL(value string) bool { return proxyBaseURL.MatchString(value) }

// TokenFromBaseURL extracts the consent token from an injected base
// URL.
func TokenFromBaseURL(value string) (string, bool) {
	m := proxyBaseURL.FindStringSubmatch(value)
	if m == nil {
		return "", false
	}
	return m[2], true
}

// InjectProject merges a project injection into the settings file at
// path: the three session hooks, and the proxy base URL when baseURL
// is not empty. An empty baseURL installs the WithoutProxy shape.
// After it returns the file carries exactly the shape asked for: a
// trajector hook spelled for the other shape, or for an older
// executable path, is replaced rather than kept beside the new one.
//
// The base URL of a file that already carries one is never dropped
// here: that key is where the user's own configuration lives, and
// removal is the one operation that knows what to put back. A file
// still carrying an injected base URL therefore refuses the
// WithoutProxy shape with errBaseURLInjected; remove first.
//
// Writing the base URL is the one write that leaves a consent token in
// the file, so that form goes through editSecret; the other leaves the
// file's mode alone.
func InjectProject(path string, baseURL string, hooks HookCommands) error {
	ensureProxy := hooks.EnsureProxy
	if baseURL == "" {
		ensureProxy += " " + NoProxyMarker
	}
	wanted := map[string]string{
		eventSessionStart:     ensureProxy,
		eventUserPromptSubmit: ensureProxy,
		eventSessionEnd:       hooks.SessionEnd,
	}
	mutate := func(root map[string]any) error {
		if baseURL != "" {
			env, err := childObject(root, "env")
			if err != nil {
				return err
			}
			env[envBaseURL] = baseURL
		} else if value, _ := envValue(root, envBaseURL); isProxyBaseURL(value) {
			return fmt.Errorf("%w: %s", errBaseURLInjected, path)
		}
		eachHookEntry(root, func(event string, entry map[string]any) hookAction {
			cmd, _ := entry["command"].(string)
			if !isProjectHook(cmd) || cmd == wanted[event] {
				return keepEntry
			}
			return dropEntry
		})
		for _, event := range []string{eventSessionStart, eventUserPromptSubmit, eventSessionEnd} {
			if err := addHook(root, event, wanted[event]); err != nil {
				return err
			}
		}
		return nil
	}
	if baseURL == "" {
		return edit(path, mutate)
	}
	return editSecret(path, mutate)
}

// isProjectHook reports whether a hook command is one a project
// injection installs, in either shape.
func isProjectHook(cmd string) bool {
	for _, marker := range projectMarkers {
		if strings.Contains(cmd, marker) {
			return true
		}
	}
	return false
}

// RemoveProject deletes the injected base URL and every trajector
// session hook, in either shape, from the settings file at path. A
// missing file is already-removed.
func RemoveProject(path string) error {
	return removeInjection(path, true, projectMarkers...)
}

// InjectionShape reports which shape of injection stands in the
// settings file at path, and false when nothing of trajector's does.
// It is a reading of the file, never the answer to what shape a
// project records in: the grant holds that. An injected base URL
// decides WithProxy on its own, hooks or no hooks: it is the part that
// routes traffic, so it is the part a repair must reason from. Without
// one, an ensure-proxy hook marked NoProxyMarker decides WithoutProxy.
// Unmarked hooks with no base URL are not a shape: whichever form put
// them there has lost the part that distinguished it.
func InjectionShape(path string) (routing.Shape, bool) {
	root, err := readSettings(path)
	if err != nil {
		return "", false
	}
	if value, _ := envValue(root, envBaseURL); isProxyBaseURL(value) {
		return routing.WithProxy, true
	}
	marked := false
	eachHookEntry(root, func(_ string, entry map[string]any) hookAction {
		if cmd, _ := entry["command"].(string); strings.Contains(cmd, EnsureProxyMarker) && hasArgument(cmd, NoProxyMarker) {
			marked = true
		}
		return keepEntry
	})
	if marked {
		return routing.WithoutProxy, true
	}
	return "", false
}

// hasArgument reports whether a shell command carries arg as one of
// its whitespace-separated words.
func hasArgument(cmd, arg string) bool {
	for _, word := range strings.Fields(cmd) {
		if word == arg {
			return true
		}
	}
	return false
}

// SetBaseURL writes value as this settings file's own base URL. It
// exists for one caller: removal has to be able to put back a value the
// injection displaced, because injection writes into the very key — in
// the very file — the user's own configuration lives in.
func SetBaseURL(path, value string) error {
	return edit(path, func(root map[string]any) error {
		env, err := childObject(root, "env")
		if err != nil {
			return err
		}
		env[envBaseURL] = value
		return nil
	})
}

// InjectedBaseURL reads the trajector-injected base URL from the
// settings file at path.
func InjectedBaseURL(path string) (string, bool) {
	root, err := readSettings(path)
	if err != nil {
		return "", false
	}
	value, ok := envValue(root, envBaseURL)
	if !ok || !isProxyBaseURL(value) {
		return "", false
	}
	return value, true
}

// envValue reads the string value of key from root's env block.
func envValue(root map[string]any, key string) (string, bool) {
	env, ok := root["env"].(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := env[key].(string)
	return value, ok && value != ""
}

// InjectUserHook merges the discovery hook into the user settings file
// at path.
func InjectUserHook(path, hookCommand string) error {
	return edit(path, func(root map[string]any) error {
		return addHook(root, eventSessionStart, hookCommand)
	})
}

// RemoveUserHook deletes every trajector discovery hook from the user
// settings file at path.
func RemoveUserHook(path string) error {
	return removeInjection(path, false, DiscoveryMarker)
}

// HasHook reports whether the settings file at path carries a hook
// whose command contains marker.
func HasHook(path, marker string) bool {
	root, err := readSettings(path)
	if err != nil {
		return false
	}
	found := false
	eachHookEntry(root, func(_ string, entry map[string]any) hookAction {
		if cmd, _ := entry["command"].(string); strings.Contains(cmd, marker) {
			found = true
		}
		return keepEntry
	})
	return found
}

func removeInjection(path string, dropEnv bool, markers ...string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return edit(path, func(root map[string]any) error {
		if dropEnv {
			dropInjectedEnv(root)
		}
		eachHookEntry(root, func(_ string, entry map[string]any) hookAction {
			cmd, _ := entry["command"].(string)
			for _, marker := range markers {
				if strings.Contains(cmd, marker) {
					return dropEntry
				}
			}
			return keepEntry
		})
		return nil
	})
}

func dropInjectedEnv(root map[string]any) {
	env, ok := root["env"].(map[string]any)
	if !ok {
		return
	}
	if value, _ := env[envBaseURL].(string); !isProxyBaseURL(value) {
		return
	}
	delete(env, envBaseURL)
	if len(env) == 0 {
		delete(root, "env")
	}
}

func addHook(root map[string]any, event, command string) error {
	hooks, err := childObject(root, "hooks")
	if err != nil {
		return err
	}
	groups, ok := hooks[event].([]any)
	if hooks[event] != nil && !ok {
		// Refusing beats guessing, as in childObject: appending would
		// destroy the malformed value, skipping would report success
		// without installing the hook.
		return fmt.Errorf("claudesettings: hooks.%s is not a list", event)
	}
	exists := false
	eachHookEntry(root, func(e string, entry map[string]any) hookAction {
		if cmd, _ := entry["command"].(string); e == event && cmd == command {
			exists = true
		}
		return keepEntry
	})
	if exists {
		return nil
	}
	hooks[event] = append(groups, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	})
	return nil
}

// hookAction is a visitor's verdict on one hook entry.
type hookAction bool

const (
	keepEntry hookAction = true
	dropEntry hookAction = false
)

// eachHookEntry visits every well-formed hook entry under root
// (hooks → event → group list → group's hooks list → entry) and drops
// the entries the visitor rejects, pruning any group, event, or hooks
// section its drops leave empty. Nodes that do not match the expected
// shape are skipped and kept as they are: the walk never errors and
// never rewrites what it cannot parse.
func eachHookEntry(root map[string]any, visit func(event string, entry map[string]any) hookAction) {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return
	}
	pruned := false
	for event, groups := range hooks {
		list, ok := groups.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, g := range list {
			group, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			entries, ok := group["hooks"].([]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			var keptEntries []any
			for _, e := range entries {
				entry, ok := e.(map[string]any)
				if !ok || visit(event, entry) == keepEntry {
					keptEntries = append(keptEntries, e)
				}
			}
			if len(keptEntries) == len(entries) {
				kept = append(kept, g)
				continue
			}
			if len(keptEntries) == 0 {
				continue
			}
			group["hooks"] = keptEntries
			kept = append(kept, g)
		}
		if len(kept) == len(list) {
			continue
		}
		if len(kept) == 0 {
			delete(hooks, event)
			pruned = true
			continue
		}
		hooks[event] = kept
	}
	if pruned && len(hooks) == 0 {
		delete(root, "hooks")
	}
}

func childObject(root map[string]any, key string) (map[string]any, error) {
	if root[key] == nil {
		obj := map[string]any{}
		root[key] = obj
		return obj, nil
	}
	obj, ok := root[key].(map[string]any)
	if !ok {
		// Refusing beats guessing: overwriting a malformed section could
		// destroy user configuration.
		return nil, fmt.Errorf("claudesettings: %q is not an object", key)
	}
	return obj, nil
}

func readSettings(path string) (map[string]any, error) {
	// fsatomic.ReadFile, not os.ReadFile: editFile replaces this path by
	// rename, and fsatomic states the rule without qualification — a plain
	// read crossing that rename fails on Windows with a sharing violation,
	// and the plain reader's own handle in turn blocks the rename. Every
	// caller here (InjectedBaseURL, HasHook, TopLevelBool, fileEnv)
	// swallows that error into "not injected" / "no hook", so the failure
	// surfaces as doctor reporting an injection problem that does not
	// exist and rewriting a settings file that was already correct. The
	// injected hooks run trajector on every prompt, so two processes
	// touching these files at once is the normal case, not a corner.
	// project.go moved .gitignore across for this reason on 2026-08-27;
	// the settings files were missed. 2026-09-13.
	data, err := fsatomic.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return parseSettings(path, data)
}

func parseSettings(path string, data []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("claudesettings: parsing %s: %w", path, err)
	}
	return root, nil
}

// errUnchanged, returned by a mutate callback, aborts an edit without
// writing: the file the user formatted stays byte-for-byte as it is
// instead of being re-marshalled.
var errUnchanged = errors.New("claudesettings: no change")

// errSymlinked reports a settings file that is a symbolic link, which
// this package refuses to edit.
var errSymlinked = errors.New("settings file is a symbolic link")

// RefuseSymlink stops a write that would replace a symbolic link.
// Every write here ends in a rename, and a rename installs over the
// link itself, not over what it points at: a file the user keeps as a
// link into a dotfiles repository silently became a detached regular
// file the managed copy never saw. Refusing is the judgment this
// package already makes for a symlinked .gitignore, for the same
// reason — a link is a path someone else chose. 2026-09-10.
func RefuseSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s (nothing was written; point it at a real file or remove the link)", errSymlinked, path)
}

// edit is a read-modify-write of a file whose other writers are the
// user and concurrent trajector processes; a plain last-write-wins
// replacement would discard whatever a concurrent writer merged in, so
// the whole cycle runs under fsatomic.Update's cross-process lock.
func edit(path string, mutate func(map[string]any) error) error {
	return editFile(path, false, mutate)
}

// editSecret is edit for a write that leaves this project's consent
// token in the file: the mode is additionally narrowed to owner-only.
//
// "New files are owner-only because the injected URL embeds a consent
// token" was the whole of that rule until 2026-09-06, and it protected
// the rarer case. Claude Code creates .claude/settings.local.json itself
// — it is where the permissions a user approves are stored — so under a
// default umask the file injection lands in already exists at 0644, and
// preserving the user's mode preserved that. The token is a capability,
// not a label: the proxy binds loopback, which every account on the
// machine can reach, so anyone who can read the token can have traffic
// of their own recorded into this project and uploaded under this
// device. Narrowing is confined to the writes that put the token there,
// because widening it back once RemoveProject has taken the token out
// would be this tool deciding the mode of a file that is the user's.
func editSecret(path string, mutate func(map[string]any) error) error {
	return editFile(path, true, mutate)
}

func editFile(path string, secret bool, mutate func(map[string]any) error) error {
	if err := RefuseSymlink(path); err != nil {
		return err
	}
	// Keep the user's chosen permissions on an existing file; new files
	// are owner-only because the injected URL embeds a consent token.
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if secret {
		// Only group and other are cleared. The owner's own bits are
		// whatever they were, so a file the user made read-only for
		// themselves stays that way.
		mode &^= 0o077
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	err := fsatomic.Update(path, mode, func(old []byte) ([]byte, error) {
		root, err := parseSettings(path, old)
		if err != nil {
			return nil, err
		}
		if err := mutate(root); err != nil {
			return nil, err
		}
		data, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(data, '\n'), nil
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	return err
}
