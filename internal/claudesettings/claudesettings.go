// Package claudesettings edits and reads Claude Code settings files on
// trajector's behalf. All edits are merges: the user's own settings
// content is preserved byte-for-byte at the JSON level, and removal
// deletes exactly what trajector injected, nothing else.
package claudesettings

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// envBaseURL is the environment key Claude Code reads for its API base
// URL; injecting it is how a project's traffic is routed through the
// local proxy.
const envBaseURL = "ANTHROPIC_BASE_URL"

// Hook events used for injection.
const (
	eventSessionStart     = "SessionStart"
	eventUserPromptSubmit = "UserPromptSubmit"
)

// Marker substrings identifying trajector-injected hook commands, so
// removal never touches a hook the user wrote themselves.
const (
	EnsureProxyMarker = "hook ensure-proxy"
	DiscoveryMarker   = "hook discovery"
)

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

// InjectProject merges the proxy base URL and the two ensure-proxy
// hooks into the settings file at path.
func InjectProject(path, baseURL, hookCommand string) error {
	return edit(path, func(root map[string]any) error {
		env, err := childObject(root, "env")
		if err != nil {
			return err
		}
		env[envBaseURL] = baseURL
		if err := addHook(root, eventSessionStart, hookCommand); err != nil {
			return err
		}
		return addHook(root, eventUserPromptSubmit, hookCommand)
	})
}

// RemoveProject deletes the injected base URL and every trajector
// ensure-proxy hook from the settings file at path. A missing file is
// already-removed.
func RemoveProject(path string) error {
	return removeInjection(path, true, EnsureProxyMarker)
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

func removeInjection(path string, dropEnv bool, marker string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return edit(path, func(root map[string]any) error {
		if dropEnv {
			dropInjectedEnv(root)
		}
		eachHookEntry(root, func(_ string, entry map[string]any) hookAction {
			if cmd, _ := entry["command"].(string); strings.Contains(cmd, marker) {
				return dropEntry
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
	data, err := os.ReadFile(path)
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

// edit is a read-modify-write of a file whose other writers are the
// user and concurrent trajector processes; a plain last-write-wins
// replacement would discard whatever a concurrent writer merged in, so
// the whole cycle runs under fsatomic.Update's cross-process lock.
func edit(path string, mutate func(map[string]any) error) error {
	// Keep the user's chosen permissions on an existing file; new files
	// are owner-only because the injected URL embeds a consent token.
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsatomic.Update(path, mode, func(old []byte) ([]byte, error) {
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
}
