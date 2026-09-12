package claudesettings

import "path/filepath"

// Source identifies where a configuration value was found. It names a
// rank of the settings chain, never the file a value happened to be
// read from: a rank is what the user can act on, and what a rank is
// spelled on disk is this package's business alone.
type Source string

const (
	SourceManagedDelivered Source = "delivered managed settings"
	SourceManaged          Source = "managed settings"
	SourceProjectLocal     Source = "project settings.local.json"
	SourceProject          Source = "project settings.json"
	SourceUser             Source = "user settings.json"
	SourceShell            Source = "shell environment"
)

// layers selects the ranks one question applies to. Which ranks apply
// differs by key — a project may not start Claude Code in a mode, and
// only an organization may lock hooks away from the other ranks — but
// the order of the ranks never differs, which is why the order is
// stated once in Host.chain and a question says only which ranks it is
// subject to.
type layers uint8

const (
	// layerManaged is the settings an organization manages for the
	// host: the cache of what Claude Code's own service delivered, and
	// the managed directory. They outrank everything a user sets.
	layerManaged layers = 1 << iota
	// layerProject is the project's own two files, local first.
	layerProject
	// layerUser is the user's own settings file.
	layerUser
	// layerShell is the environment this process was started in.
	layerShell
)

// layer is one rank: the source that stands for it, and what it reads.
// A rank backed by several files — the managed directory holds a base
// file and a drop-in directory beside it — is decided by the last file
// that sets the key, the way Claude Code merges them. The shell rank
// reads no file and answers env keys alone.
type layer struct {
	source Source
	roots  []map[string]any
	shell  func(string) string
}

// chain is the ranks that apply to one question, highest-deciding
// first.
type chain []layer

// decision is what a walk of the chain found: the key that decided,
// the value it decided with, and the rank it came from.
type decision struct {
	key    string
	value  any
	source Source
}

// decide walks the chain from the highest rank down and returns the
// first decision it finds: a rank that sets one of keys, to a value
// accept takes. A value accept refuses is not a decision and the walk
// goes on below it, which is how a question says "keep looking"
// instead of taking a refused value for its answer. Keys are tried
// within a rank, so a higher rank decides over a lower one whichever
// of the keys each sets. read says how a key is taken from one file.
func (c chain) decide(keys []string, read reader, accept func(key string, value any) bool) (decision, bool) {
	for _, l := range c {
		for _, key := range keys {
			if value, found := l.lookup(key, read); found && accept(key, value) {
				return decision{key: key, value: value, source: l.source}, true
			}
		}
	}
	return decision{}, false
}

func (l layer) lookup(key string, read reader) (any, bool) {
	if l.shell != nil {
		value := l.shell(key)
		return value, value != ""
	}
	for i := len(l.roots) - 1; i >= 0; i-- {
		if value, found := read(l.roots[i], key); found {
			return value, true
		}
	}
	return nil, false
}

// chain builds the ranks want selects, in the one order Claude Code
// ranks them: the settings an organization manages for the host, then
// the project's own files from the narrowest scope out, then the
// user's file, and the shell last. Every file is read afresh, so an
// answer never describes an earlier state of the host. getenv is read
// only by the shell rank.
func (h Host) chain(projectRoot string, getenv func(string) string, want layers) chain {
	var c chain
	if want&layerManaged != 0 {
		c = append(c, layer{source: SourceManagedDelivered, roots: readRoots(h.deliveredSettingsPath())})
		if files := h.managedSettingsFiles(); len(files) > 0 {
			c = append(c, layer{source: SourceManaged, roots: readRoots(files...)})
		}
	}
	if want&layerProject != 0 {
		c = append(c,
			layer{source: SourceProjectLocal, roots: readRoots(ProjectLocalPath(projectRoot))},
			layer{source: SourceProject, roots: readRoots(projectSharedPath(projectRoot))})
	}
	if want&layerUser != 0 {
		c = append(c, layer{source: SourceUser, roots: readRoots(h.UserSettingsPath())})
	}
	if want&layerShell != 0 {
		c = append(c, layer{source: SourceShell, shell: getenv})
	}
	return c
}

func readRoots(paths ...string) []map[string]any {
	roots := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		roots = append(roots, readRoot(path))
	}
	return roots
}

// readRoot reads one settings file for a walk. A file that is missing,
// unreadable, or malformed reads as a nil root, which answers no
// lookup: a broken file says nothing, and never decides a question by
// being broken. That is why the error is swallowed here and nowhere
// else in the walk.
func readRoot(path string) map[string]any {
	root, err := readSettings(path)
	if err != nil {
		return nil
	}
	return root
}

// reader takes one key from a settings file. The readings this package
// needs are a top-level key of any type, a top-level boolean, and an
// entry of the env block. Each answers "not found" for a key the file
// does not set, and for the nil root of a file that could not be read.
type reader func(root map[string]any, key string) (any, bool)

func topLevelValue(root map[string]any, key string) (any, bool) {
	value, found := root[key]
	return value, found
}

// topLevelBoolValue reads a top-level key that must be a boolean. A
// value of any other type reads as absent, so a hand-edited string
// never decides a question asked of a boolean.
func topLevelBoolValue(root map[string]any, key string) (any, bool) {
	value, found := root[key].(bool)
	return value, found
}

func envEntry(root map[string]any, key string) (any, bool) {
	value, found := envValue(root, key)
	return value, found
}

// anyValue takes whatever a rank sets, which makes the highest rank
// that sets the key the one that decides it.
func anyValue(string, any) bool { return true }

// envChain is the ranks an environment variable is resolved over: a
// settings env block overrides the shell environment unconditionally,
// and the narrower scope wins.
func (h Host) envChain(projectRoot string, getenv func(string) string) chain {
	return h.chain(projectRoot, getenv, layerProject|layerUser|layerShell)
}

// envDecision reshapes a decision over an env key, whose value is
// always a string.
func envDecision(d decision, found bool) (string, Source, bool) {
	if !found {
		return "", "", false
	}
	return d.value.(string), d.source, true
}

// effectiveEnv resolves key to the value Claude Code would see for it.
func effectiveEnv(projectRoot string, host Host, key string, getenv func(string) string) (string, Source, bool) {
	return envDecision(host.envChain(projectRoot, getenv).decide([]string{key}, envEntry, anyValue))
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
func ExternalBaseURL(projectRoot string, host Host, getenv func(string) string) (string, Source, BaseURLResolution) {
	value, source, found := envDecision(host.envChain(projectRoot, getenv).
		decide([]string{envBaseURL}, envEntry, func(_ string, value any) bool {
			return !isProxyBaseURL(value.(string))
		}))
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
func UnsupportedChannel(projectRoot string, host Host, getenv func(string) string) (string, bool) {
	for _, key := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		if value, _, ok := effectiveEnv(projectRoot, host, key, getenv); ok && truthy(value) {
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
