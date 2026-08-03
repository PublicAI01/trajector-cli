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

// EnvBaseURL is the environment key Claude Code reads for its API base
// URL; injecting it is how a project's traffic is routed through the
// local proxy.
const EnvBaseURL = "ANTHROPIC_BASE_URL"

// Hook events used for injection.
const (
	EventSessionStart     = "SessionStart"
	EventUserPromptSubmit = "UserPromptSubmit"
)

// Marker substrings identifying trajector-injected hook commands, so
// removal never touches a hook the user wrote themselves.
const (
	EnsureProxyMarker = "hook ensure-proxy"
	DiscoveryMarker   = "hook discovery"
)

// ProjectLocalPath locates the project-scoped settings file that
// receives the injection. It is local (never committed) by Claude Code
// convention; enable additionally verifies gitignore coverage.
func ProjectLocalPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".claude", "settings.local.json")
}

// UserSettingsPath locates the user-scoped settings file that receives
// the discovery hook.
func UserSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// proxyBaseURL recognizes a base URL injected by trajector: loopback
// host with a /t/<token> path. Matching stays narrow so removal can
// never mistake a user's own relay URL for our injection.
var proxyBaseURL = regexp.MustCompile(`^http://(127\.0\.0\.1|localhost|\[::1\]):[0-9]+/t/([A-Za-z0-9._-]+)$`)

// IsProxyBaseURL reports whether value is a trajector-injected base
// URL.
func IsProxyBaseURL(value string) bool { return proxyBaseURL.MatchString(value) }

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
		env[EnvBaseURL] = baseURL
		if err := addHook(root, EventSessionStart, hookCommand); err != nil {
			return err
		}
		return addHook(root, EventUserPromptSubmit, hookCommand)
	})
}

// RemoveProject deletes the injected base URL and every trajector
// ensure-proxy hook from the settings file at path. A missing file is
// already-removed.
func RemoveProject(path string) error {
	return removeInjection(path, true, EnsureProxyMarker)
}

// InjectedBaseURL reads the trajector-injected base URL from the
// settings file at path.
func InjectedBaseURL(path string) (string, bool) {
	root, err := readSettings(path)
	if err != nil {
		return "", false
	}
	env, ok := root["env"].(map[string]any)
	if !ok {
		return "", false
	}
	value, _ := env[EnvBaseURL].(string)
	if !IsProxyBaseURL(value) {
		return "", false
	}
	return value, true
}

// InjectUserHook merges the discovery hook into the user settings file
// at path.
func InjectUserHook(path, hookCommand string) error {
	return edit(path, func(root map[string]any) error {
		return addHook(root, EventSessionStart, hookCommand)
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
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return false
	}
	for _, groups := range hooks {
		list, ok := groups.([]any)
		if !ok {
			continue
		}
		for _, g := range list {
			group, ok := g.(map[string]any)
			if !ok {
				continue
			}
			entries, _ := group["hooks"].([]any)
			for _, e := range entries {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if cmd, _ := entry["command"].(string); strings.Contains(cmd, marker) {
					return true
				}
			}
		}
	}
	return false
}

func removeInjection(path string, dropEnv bool, marker string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return edit(path, func(root map[string]any) error {
		if dropEnv {
			if env, ok := root["env"].(map[string]any); ok {
				if value, _ := env[EnvBaseURL].(string); IsProxyBaseURL(value) {
					delete(env, EnvBaseURL)
				}
				if len(env) == 0 {
					delete(root, "env")
				}
			}
		}
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			return nil
		}
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
				entries, _ := group["hooks"].([]any)
				var keptEntries []any
				for _, e := range entries {
					entry, ok := e.(map[string]any)
					if ok {
						if cmd, _ := entry["command"].(string); strings.Contains(cmd, marker) {
							continue
						}
					}
					keptEntries = append(keptEntries, e)
				}
				if len(keptEntries) == 0 && len(entries) > 0 {
					continue
				}
				group["hooks"] = keptEntries
				kept = append(kept, group)
			}
			if len(kept) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = kept
			}
		}
		if len(hooks) == 0 {
			delete(root, "hooks")
		}
		return nil
	})
}

func addHook(root map[string]any, event, command string) error {
	hooks, err := childObject(root, "hooks")
	if err != nil {
		return err
	}
	groups, ok := hooks[event].([]any)
	if hooks[event] != nil && !ok {
		return fmt.Errorf("claudesettings: hooks.%s is not a list", event)
	}
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := entry["command"].(string); cmd == command {
				return nil
			}
		}
	}
	hooks[event] = append(groups, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	})
	return nil
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
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("claudesettings: parsing %s: %w", path, err)
	}
	return root, nil
}

func edit(path string, mutate func(map[string]any) error) error {
	root, err := readSettings(path)
	if err != nil {
		return err
	}
	if err := mutate(root); err != nil {
		return err
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Keep the user's chosen permissions on an existing file; new files
	// are owner-only because the injected URL embeds a consent token.
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsatomic.WriteFile(path, data, mode)
}
