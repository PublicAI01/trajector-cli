package repoassess

import (
	"regexp"
	"slices"
	"strings"
)

// ruleVersion names the rules in this file. It changes whenever a rule
// is added, removed or edited, so that two readouts with the same
// version were computed by the same rules and can be compared.
const ruleVersion = 1

// kind is what a path holds, read from the path alone.
type kind int

const (
	// other is a path that is neither a test nor implementation code:
	// documentation, data, configuration, and the example, vendored and
	// generated paths that excludeDirRule and generatedFileRule name.
	other kind = iota
	// test is a path that a test rule matches, whether or not it is
	// code.
	test
	// src is implementation code: a code extension, and no test rule
	// matches.
	src
)

// codeExtensions maps a lowercase file extension to its language. A
// path whose extension is not here is not code.
var codeExtensions = map[string]string{
	"py": "python", "pyx": "python", "go": "go", "js": "js", "jsx": "js", "mjs": "js", "cjs": "js",
	"ts": "ts", "tsx": "ts", "mts": "ts", "cts": "ts", "vue": "js", "svelte": "js", "rs": "rust",
	"java": "java", "kt": "kotlin", "kts": "kotlin", "scala": "scala", "sc": "scala", "php": "php",
	"rb": "ruby", "jl": "julia", "ex": "elixir", "exs": "elixir", "erl": "erlang", "swift": "swift",
	"clj": "clojure", "cljs": "clojure", "cljc": "clojure", "dart": "dart", "c": "c", "h": "c",
	"cc": "cpp", "cpp": "cpp", "cxx": "cpp", "hpp": "cpp", "hh": "cpp", "hxx": "cpp", "cs": "csharp",
	"fs": "fsharp", "r": "r", "ml": "ocaml", "mli": "ocaml", "lua": "lua", "m": "objc", "mm": "objc",
	"hs": "haskell", "zig": "zig", "nim": "nim", "groovy": "groovy", "pl": "perl", "pm": "perl",
	"sh": "shell", "bash": "shell",
}

// testDirNames are directory names that make every path below them a
// test path. Each entry matches one whole path segment.
var testDirNames = []string{
	`[Tt]ests?`, `TESTS?`, `__tests__`, `[Ss]pecs?`, `[Tt]esting`, `testdata`, `test_data`, `test-data`,
	`fixtures`, `e2e`, `[a-z0-9]+Tests?`, `[A-Za-z0-9]+[-_][Tt]ests?`, `[Tt]ests?[-_][A-Za-z0-9_-]+`,
	`[Uu]nit[Tt]ests?`,
}

// testFileNames are file-name shapes that make a path a test path,
// wherever it stands. Each entry matches one whole last segment.
var testFileNames = []string{
	`test_[^/]*\.py`,
	`[^/]*_test\.(py|go|rb|exs|dart|cc|cpp|c|rs|lua|ml)`,
	`[^/]*\.(test|spec)\.[jt]sx?`,
	`[^/]*\.(test|spec)\.m[jt]s`,
	`[^/]*(Test|Tests|Spec|IT)\.(java|kt|scala|cs|swift|php|groovy)`,
	`Test[A-Z][^/]*\.(java|kt|scala|cs|swift)`,
	`[^/]*_spec\.(rb|lua)`,
	`test[^/]*\.jl`,
	`runtests\.jl`,
	`[^/]*_tests?\.rs`,
	`test-[^/]*\.(el|sh|js|ts)`,
	`[^/]*Test[^/]*\.php`,
}

// ciPaths are path prefixes, from the repository root, of continuous
// integration configuration.
var ciPaths = []string{
	`\.github/workflows/[^/]+\.ya?ml`, `\.gitlab-ci\.yml`, `\.travis\.yml`, `\.circleci/`,
	`azure-pipelines\.ya?ml`, `Jenkinsfile`, `appveyor\.ya?ml`, `\.buildkite/`, `bitbucket-pipelines\.yml`,
	`\.drone\.yml`, `\.woodpecker`, `\.cirrus\.yml`, `\.github/actions/`,
}

// manifestLocks maps a dependency manifest's file name to the lockfile
// names that pin it. A manifest with no entry pins nothing.
var manifestLocks = map[string][]string{
	"package.json":     {"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "bun.lock", "npm-shrinkwrap.json"},
	"pyproject.toml":   {"poetry.lock", "uv.lock", "pdm.lock", "Pipfile.lock", "requirements.lock", "pixi.lock"},
	"Pipfile":          {"Pipfile.lock"},
	"go.mod":           {"go.sum"},
	"Cargo.toml":       {"Cargo.lock"},
	"Gemfile":          {"Gemfile.lock"},
	"composer.json":    {"composer.lock"},
	"mix.exs":          {"mix.lock"},
	"pubspec.yaml":     {"pubspec.lock"},
	"Package.swift":    {"Package.resolved"},
	"Project.toml":     {"Manifest.toml"},
	"build.gradle":     {"gradle.lockfile", "gradle-wrapper.properties", "versions.lock"},
	"build.gradle.kts": {"gradle.lockfile", "gradle-wrapper.properties", "versions.lock"},
	"pom.xml":          {"maven-wrapper.properties"},
	"deps.edn":         nil,
	"project.clj":      nil,
	"dune-project":     {"opam.locked"},
	"DESCRIPTION":      {"renv.lock"},
	"rockspec":         nil,
	"CMakeLists.txt":   {"conan.lock", "vcpkg.json"},
	"setup.py":         nil,
	"requirements.txt": nil,
}

// manifestSuffixes are file-name endings that also make a manifest.
var manifestSuffixes = []string{".rockspec", ".csproj", ".opam"}

// extraLocks are lockfile names that pin no manifest in manifestLocks.
var extraLocks = []string{"packages.lock.json"}

// manifestMaxDepth is how many directories deep a manifest or lockfile
// may sit and still count as the repository's own.
const manifestMaxDepth = 2

// dockerFiles are file names that describe a container build, at any
// depth; a name that starts with "Dockerfile." counts too.
var dockerFiles = []string{"Dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yaml"}

// excludeDirRule names directories whose files are neither the
// repository's own implementation nor its tests: example and demo code
// (samples only at the root), vendored code, generated output, and the
// root documentation directory. generatedFileRule names generated files
// by their file-name conventions.
//
// Partly adapted from github-linguist vendor.yml, documentation.yml and
// generated.rb, MIT License, Copyright (c) 2017 GitHub, Inc. Linguist
// also reads file headers to find generated files; these rules read
// the path alone.
var (
	excludeDirRule = regexp.MustCompile(`(^|/)([Ee]xamples?|[Dd]emos?)/|^[Ss]amples?/` +
		`|(^|/)([Vv]endors?|(3rd|[Tt]hird)[-_]?[Pp]arty|node_modules|bower_components)/` +
		`|(^|/)(__generated__|generated|dist)/` +
		`|^([Dd]ocs?|[Dd]ocumentation)/`)
	generatedFileRule = regexp.MustCompile(`(\.pb\.(go|cc|h|swift)|\.pb\.gw\.go|_pb2(_grpc)?\.pyi?|_(grpc_)?pb\.[jt]s|\.(g|freezed|gr|mocks)\.dart` +
		`|[._]generated\.[A-Za-z]+|zz_generated[^/]*\.go|\.designer\.cs|\.g\.cs|[.-]min\.js)$`)
)

var (
	testDirRule  = regexp.MustCompile(`(^|/)(` + strings.Join(testDirNames, "|") + `)(/|$)`)
	testFileRule = regexp.MustCompile(`(^|/)(` + strings.Join(testFileNames, "|") + `)$`)
	ciRule       = regexp.MustCompile(`^(` + strings.Join(ciPaths, "|") + `)`)
	lockNames    = lockSet()
)

func lockSet() map[string]bool {
	set := map[string]bool{}
	for _, locks := range manifestLocks {
		for _, l := range locks {
			set[l] = true
		}
	}
	for _, l := range extraLocks {
		set[l] = true
	}
	return set
}

// classify reads what a repository path holds and, for code, its
// language. A test path that is code has a language too.
func classify(path string) (kind, string) {
	base := baseName(path)
	ext := ""
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		ext = strings.ToLower(base[i+1:])
	}
	lang := codeExtensions[ext]
	// An excluded directory wins over a test directory only when it
	// stands earlier in the path: examples/x/test/a.py is excluded, and
	// tests/examples/a.py is a test.
	if ex := excludeDirRule.FindStringIndex(path); ex != nil {
		if td := testDirRule.FindStringIndex(path); td == nil || ex[0] < td[0] {
			return other, ""
		}
	}
	if testFileRule.MatchString(path) || testDirRule.MatchString(path) {
		return test, lang
	}
	// A generated file in a test directory is still a test path.
	if generatedFileRule.MatchString(path) {
		return other, ""
	}
	if lang != "" {
		return src, lang
	}
	return other, ""
}

func isCI(path string) bool { return ciRule.MatchString(path) }

func isManifest(base string) bool {
	if _, ok := manifestLocks[base]; ok {
		return true
	}
	for _, s := range manifestSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

func isDocker(base string) bool {
	return slices.Contains(dockerFiles, base) || strings.HasPrefix(base, "Dockerfile.")
}

func baseName(path string) string {
	return path[strings.LastIndexByte(path, '/')+1:]
}

// agentAddresses are email addresses coding agents commit under, as
// the author or in a Co-authored-by trailer.
var agentAddresses = []string{
	"noreply@anthropic.com",
	"cursoragent@cursor.com",
	"aider@aider.chat",
	"openhands@all-hands.dev",
}

// agentAccounts are GitHub account names coding agents commit under.
// GitHub's no-reply address puts a numeric account id before the name,
// and only the name is compared.
var agentAccounts = []string{
	"copilot",
	"copilot-swe-agent[bot]",
	"claude[bot]",
	"cursor[bot]",
	"devin-ai-integration[bot]",
	"chatgpt-codex-connector[bot]",
	"google-labs-jules[bot]",
	"amazon-q-developer[bot]",
	"openhands-agent",
}

const gitHubNoReplyDomain = "@users.noreply.github.com"

// isAgentAddress reports whether a lowercase email address is one a
// coding agent commits under.
func isAgentAddress(addr string) bool {
	if slices.Contains(agentAddresses, addr) {
		return true
	}
	local, ok := strings.CutSuffix(addr, gitHubNoReplyDomain)
	if !ok {
		return false
	}
	if _, name, found := strings.Cut(local, "+"); found {
		local = name
	}
	return slices.Contains(agentAccounts, local)
}
