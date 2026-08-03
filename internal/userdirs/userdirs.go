// Package userdirs owns where trajector puts files on a user's machine.
// Both halves of that question live here: which per-user directories the
// platform dictates, and which file each kind of trajector state goes
// in. No other package may name a trajector file or spool directory.
package userdirs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const appDir = "trajector"

// On-disk names. Each appears exactly once in the repository.
const (
	routingTableName = "proxy_projects.json"
	consentFileName  = "consent.json"
	spoolDirName     = "rawcalls"
	secretsDirName   = "secrets"
	proxyLogName     = "proxy.log"
)

// Env is the machine a Layout is resolved against.
type Env struct {
	GOOS   string
	Getenv func(string) string
}

// Host is the environment of the running process.
func Host() Env {
	return Env{GOOS: runtime.GOOS, Getenv: os.Getenv}
}

// Layout is where this user's trajector files live. It is resolved once
// and passed down; the directories it holds are not created by resolving
// it.
type Layout struct {
	// config holds settings, consent records, and the routing table.
	config string
	// data holds the capture spool.
	data string
	// state holds runtime state such as logs.
	state string
}

// Resolve locates the trajector directories for env without creating
// them. XDG_* environment variables override platform defaults on every
// platform so tests and headless setups can relocate every trajector
// file.
func Resolve(env Env) (Layout, error) {
	if l, ok := xdgOverrides(env.Getenv); ok {
		return l, nil
	}
	switch env.GOOS {
	case "windows":
		return windowsLayout(env.Getenv)
	case "darwin":
		return darwinLayout(env.Getenv)
	default:
		return unixLayout(env.Getenv)
	}
}

// RoutingTable is the token-to-project routing table read by the proxy.
func (l Layout) RoutingTable() string { return filepath.Join(l.config, routingTableName) }

// ConsentFile is the device and per-project consent record.
func (l Layout) ConsentFile() string { return filepath.Join(l.config, consentFileName) }

// SpoolDir is the capture spool root.
func (l Layout) SpoolDir() string { return filepath.Join(l.data, spoolDirName) }

// SecretsDir backs the file token store.
func (l Layout) SecretsDir() string { return filepath.Join(l.config, secretsDirName) }

// ProxyLog is where a supervised proxy's output is appended.
func (l Layout) ProxyLog() string { return filepath.Join(l.state, proxyLogName) }

// Roots lists every directory holding trajector files, deduplicated:
// the config, data, and state directories collapse to one path on some
// platforms, and deleting the same tree twice must not be mistaken for
// a second failure.
func (l Layout) Roots() []string {
	roots := make([]string, 0, 3)
	seen := map[string]bool{}
	for _, dir := range []string{l.data, l.config, l.state} {
		if !seen[dir] {
			seen[dir] = true
			roots = append(roots, dir)
		}
	}
	return roots
}

// xdgOverrides applies explicit XDG_* variables. It only takes effect
// when all three are set; partial overrides fall through to platform
// defaults, which still honor the individual variables on unix.
func xdgOverrides(getenv func(string) string) (Layout, bool) {
	config := getenv("XDG_CONFIG_HOME")
	data := getenv("XDG_DATA_HOME")
	state := getenv("XDG_STATE_HOME")
	if config == "" || data == "" || state == "" {
		return Layout{}, false
	}
	return Layout{
		config: filepath.Join(config, appDir),
		data:   filepath.Join(data, appDir),
		state:  filepath.Join(state, appDir),
	}, true
}

func unixLayout(getenv func(string) string) (Layout, error) {
	home := getenv("HOME")
	if home == "" {
		return Layout{}, errors.New("userdirs: HOME is not set")
	}
	pick := func(envName, fallback string) string {
		if v := getenv(envName); v != "" {
			return filepath.Join(v, appDir)
		}
		return filepath.Join(home, fallback, appDir)
	}
	return Layout{
		config: pick("XDG_CONFIG_HOME", ".config"),
		data:   pick("XDG_DATA_HOME", filepath.Join(".local", "share")),
		state:  pick("XDG_STATE_HOME", filepath.Join(".local", "state")),
	}, nil
}

func darwinLayout(getenv func(string) string) (Layout, error) {
	home := getenv("HOME")
	if home == "" {
		return Layout{}, errors.New("userdirs: HOME is not set")
	}
	root := filepath.Join(home, "Library", "Application Support", appDir)
	return Layout{config: root, data: root, state: root}, nil
}

func windowsLayout(getenv func(string) string) (Layout, error) {
	roaming := getenv("APPDATA")
	local := getenv("LOCALAPPDATA")
	if roaming == "" || local == "" {
		profile := getenv("USERPROFILE")
		if profile == "" {
			return Layout{}, errors.New("userdirs: APPDATA, LOCALAPPDATA, and USERPROFILE are not set")
		}
		if roaming == "" {
			roaming = filepath.Join(profile, "AppData", "Roaming")
		}
		if local == "" {
			local = filepath.Join(profile, "AppData", "Local")
		}
	}
	return Layout{
		config: filepath.Join(roaming, appDir),
		data:   filepath.Join(local, appDir),
		state:  filepath.Join(local, appDir),
	}, nil
}
