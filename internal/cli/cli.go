// Package cli implements the trajector command-line interface. It owns
// argv, exit codes, and what the user reads; everything a command
// actually does belongs to the lifecycle machine. Run is driven
// in-process by tests, so command implementations must write only to
// the provided streams, read only the provided stdin, and read
// configuration through the environment and the user config file.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// version is stamped at build time by the release pipeline, and by
// scripts/build.sh for anyone building from a checkout. It is what the
// client reports to the service on every request, and the service gates
// uploads on it, so a build that cannot say which release it is gets
// treated as one that cannot be placed.
//
// The fallback below narrows who ends up in that position. `go install
// <module>@<version>` records the version it resolved, and a build from
// a git checkout records the tag or pseudo-version the toolchain derived
// from it; a build made either way is as identifiable as a released one,
// and reporting "dev" for it would gate a user who is demonstrably on a
// real release. What is left unstamped after that is a build with no
// version control information at all, and for that one "dev" is the
// honest answer: nothing here knows which release it is, and the service
// must not assume.
var version = unstampedVersion

// unstampedVersion is what version holds when the linker did not stamp
// it. The initializer must stay a plain constant string: -X can only
// patch a variable initialized that way, and any runtime initializer
// here would run afterwards and overwrite the stamp.
const unstampedVersion = "dev"

func init() {
	if version != unstampedVersion {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			version = v
		}
	}
}

// ProxyAddrEnv overrides the proxy address, for tests and unusual
// setups. Production always uses the fixed address. Whatever it names
// still has to pass apiproxy.ValidateAddr: an override may move the
// port, never widen what the listener is reachable from.
const ProxyAddrEnv = "TRAJECTOR_PROXY_ADDR"

// NowEnv pins the machine's clock to a fixed RFC3339 instant, for
// tests. Production leaves it unset.
const NowEnv = "TRAJECTOR_NOW"

// RecordProxyStartEnv makes every process this binary would leave
// running — the proxy, and the reader a session hook hands its work to
// — be recorded in the proxy log instead of started, for a test suite
// that drives Run in its own process: the session hooks still reach
// the start, and nothing is left running that the suite would have to
// stop. Production leaves it unset and each start spawns its process.
//
// It is read from the environment, which a repository's committed
// settings reach through the session hooks, for the one reason the
// endpoint overrides are not: all it can do is stop this machine from
// recording, which sends nothing anywhere and reads nothing new.
const RecordProxyStartEnv = "TRAJECTOR_RECORD_PROXY_START"

type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// Each stream carries how much of a terminal it can show. They are
	// detected once and apart, because a terminal is a property of the
	// stream and not of the invocation: a user who reads the output on
	// screen and keeps the errors in a file must find plain text in
	// that file.
	outStyle report.Style
	errStyle report.Style
}

// noColorFlag turns colour off wherever it stands on the command line.
// It is accepted for every command because a user who pipes trajector
// into a pager does not want to learn which commands colour their
// output.
const noColorFlag = "--no-color"

// takeAnywhere strips flag from args wherever it stands, and reports
// whether it was there. A global flag is not an argument of the command
// that follows it, so the command's own argument count never sees it.
func takeAnywhere(args []string, flag string) ([]string, bool) {
	kept := make([]string, 0, len(args))
	found := false
	for _, arg := range args {
		if arg == flag {
			found = true
			continue
		}
		kept = append(kept, arg)
	}
	return kept, found
}

// Run executes the CLI and returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args, noColor := takeAnywhere(args, noColorFlag)
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	a.outStyle = report.DetectStyle(stdout, os.LookupEnv)
	a.errStyle = report.DetectStyle(stderr, os.LookupEnv)
	if noColor {
		a.outStyle = a.outStyle.WithoutColor()
		a.errStyle = a.errStyle.WithoutColor()
	}
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "--help", "-h":
		usage(stdout)
		return 0
	case "version", "--version":
		if code, answered := a.preparse("usage: trajector version", args[1:], nil); answered {
			return code
		}
		fmt.Fprintf(stdout, "trajector %s\n", version)
		return 0
	case "login":
		return a.loginCmd(args[1:])
	case "logout":
		return a.logoutCmd(args[1:])
	case "enable":
		return a.enableCmd(args[1:])
	case "disable":
		return a.disableCmd(args[1:])
	case "uninstall":
		return a.uninstallCmd(args[1:])
	case "status":
		return a.statusCmd(args[1:])
	case "doctor":
		return a.doctorCmd(args[1:])
	case "upload":
		return a.uploadCmd(args[1:])
	case "forget":
		return a.forgetCmd(args[1:])
	case "upgrade":
		return a.upgradeCmd(args[1:])
	case "hook":
		return a.hookCmd(args[1:])
	case proxylife.Command:
		return a.proxyCmd(args[1:])
	default:
		fmt.Fprintf(stderr, "trajector: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: trajector <command>

commands:
  login        pair this device (opens a browser link)
  logout       sign out; recording pauses, forwarding is unaffected
  enable       start contributing data from the current project
  disable      stop contributing from the current project [--purge]
  uninstall    remove every injection and optionally local data [--delete-data]
  status       show pairing, project, proxy, capture, and upload state
  doctor       diagnose and repair injection, hooks, proxy, and spool issues
  upload       upload captured data now [--force]
  forget       delete one session's not-yet-uploaded records from this machine [<session-id>]
  upgrade      install the newest published release over this one
  version      print the trajector version
  proxy run    run the local proxy (internal; started automatically)
  hook         session hook entry points (internal; injected by enable)

flags:
  --no-color   print no colour, whatever the terminal takes
`)
}

// runtimeEnv is everything a command needs to know about the machine
// it runs on, resolved in one place.
type runtimeEnv struct {
	layout    userdirs.Layout
	home      string
	execPath  string
	proxyAddr string
	// platformURL is empty unless the user config names an endpoint of
	// their own: which service the default is stays with the machine
	// that talks to it, not with the command line.
	platformURL string
	releasesURL string
}

func resolveEnv() (runtimeEnv, error) {
	layout, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		return runtimeEnv{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return runtimeEnv{}, err
	}
	execPath, err := os.Executable()
	if err != nil {
		return runtimeEnv{}, err
	}
	env := runtimeEnv{
		layout:      layout,
		home:        home,
		execPath:    execPath,
		proxyAddr:   proxylife.Addr,
		releasesURL: selfupdate.DefaultReleasesURL,
	}
	if addr := os.Getenv(ProxyAddrEnv); addr != "" {
		env.proxyAddr = addr
	}
	cfg, err := readUserConfig(layout.ConfigFile())
	if err != nil {
		return runtimeEnv{}, err
	}
	if cfg.PlatformURL != "" {
		env.platformURL = cfg.PlatformURL
	}
	if cfg.ReleasesURL != "" {
		env.releasesURL = cfg.ReleasesURL
	}
	return env, nil
}

// userConfig is what the user config file may override. Both entries
// name a place this machine will trust with something: where captured
// data and the device token go, and where the next binary comes from.
type userConfig struct {
	PlatformURL string `json:"platform_url"`
	ReleasesURL string `json:"releases_url"`
}

// readUserConfig reads the overrides from the user config file. Neither
// is ever read from an environment variable: a repository's committed
// settings reach this process's environment through the session hooks,
// and must not be able to choose where data goes or where a replacement
// binary comes from. An absent file means no overrides; an unreadable
// one fails the command loudly rather than silently using a default the
// user thought they had changed.
func readUserConfig(path string) (userConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return userConfig{}, nil
	}
	if err != nil {
		return userConfig{}, err
	}
	var cfg userConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return userConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return cfg, nil
}

// machine assembles the lifecycle machine for this invocation.
func (a *app) machine() (*lifecycle.Machine, error) {
	return machineAt("")
}

// machineAt is machine with the proxy address overridden, for the
// serve modes that take an --addr flag.
func machineAt(addr string) (*lifecycle.Machine, error) {
	env, err := resolveEnv()
	if err != nil {
		return nil, err
	}
	if addr != "" {
		env.proxyAddr = addr
	}
	// The listen address decides what this machine's proxy is reachable
	// from, and it arrives from --addr and from an environment variable
	// — which a repository's committed settings reach through the session
	// hooks, the same reason the endpoint overrides above are read from
	// the user config file alone. It is refused here rather than bound,
	// and refused before it can also be injected into a project's base
	// URL. 2026-08-15.
	if err := apiproxy.ValidateAddr(env.proxyAddr); err != nil {
		return nil, err
	}
	deps := lifecycle.Deps{
		Layout:      env.layout,
		PlatformURL: env.platformURL,
		Version:     version,
		ExecPath:    env.execPath,
		Releases:    env.releasesURL,
		ProxyAddr:   env.proxyAddr,
		Home:        env.home,
		Getenv:      os.Getenv,
	}
	if os.Getenv(RecordProxyStartEnv) != "" {
		deps.Spawn = proxylife.RecordStartsIn(deps.Layout.ProxyLog())
	}
	if v := os.Getenv(NowEnv); v != "" {
		at, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", NowEnv, err)
		}
		deps.Now = func() time.Time { return at }
	}
	return lifecycle.Open(deps), nil
}

// io hands the machine this invocation's streams.
func (a *app) io() lifecycle.IO {
	return lifecycle.IO{In: a.stdin, Out: a.stdout, Err: a.stderr, OutStyle: a.outStyle}
}

func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "trajector: %v\n", err)
	return 1
}

// prelude is what every command needs before it can do anything: the
// working directory and the machine.
func (a *app) prelude() (*lifecycle.Machine, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	m, err := a.machine()
	if err != nil {
		return nil, "", err
	}
	return m, cwd, nil
}

// takeFlag strips flag from the end of args, so a command can hand the
// rest to with's exact-count check. Only the trailing position is
// recognized: a flag anywhere else leaves the count wrong and the
// command answers with its usage line rather than guessing.
func takeFlag(args []string, flag string) ([]string, bool) {
	if n := len(args); n > 0 && args[n-1] == flag {
		return args[:n-1], true
	}
	return args, false
}

// takeFlags strips each of flags from the end of args, in whatever
// order they were written, and reports which were there. It is
// takeFlag for a command that accepts more than one: each pass takes
// whichever flag is now last, so no order of them leaves one behind
// and none of them is read as a positional argument.
func takeFlags(args []string, flags ...string) ([]string, map[string]bool) {
	taken := make(map[string]bool, len(flags))
	for {
		shorter := args
		for _, flag := range flags {
			var ok bool
			if shorter, ok = takeFlag(shorter, flag); ok {
				taken[flag] = true
			}
		}
		if len(shorter) == len(args) {
			return args, taken
		}
		args = shorter
	}
}

// preparse answers what every command must settle before it reads an
// argument as a value: whether the user asked for help, and whether a
// `-` prefixed argument is a flag the command does not know. Such an
// argument is a mistake, never a value, so no positional argument may
// be read until this has refused it — a command that read one would act
// on a name the user never wrote. known holds the flags the command
// itself accepts, including any it takes in a positional slot. The
// second result reports that the command is already answered.
func (a *app) preparse(usage string, args []string, known []string) (int, bool) {
	if slices.ContainsFunc(args, func(arg string) bool { return arg == "--help" || arg == "-h" }) {
		fmt.Fprintln(a.stdout, usage)
		return 0, true
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") || slices.Contains(known, arg) {
			continue
		}
		fmt.Fprintln(a.stderr, usage)
		fmt.Fprintf(a.stderr, "trajector: unknown flag %q\n", arg)
		return 2, true
	}
	return 0, false
}

// with is the shape every command shares: the flag pre-parse, the
// argument-count check against usage, the prelude, and the one mapping
// from the machine's answer to an exit code. known names the flags this
// command still accepts at this point: the ones takeFlag already
// stripped are gone, and what remains is a flag the command reads from
// a positional slot.
func (a *app) with(usage string, args []string, nargs int, do func(m *lifecycle.Machine, cwd string) error, known ...string) int {
	if code, answered := a.preparse(usage, args, known); answered {
		return code
	}
	if len(args) != nargs {
		fmt.Fprintln(a.stderr, usage)
		return 2
	}
	m, cwd, err := a.prelude()
	if err != nil {
		return a.fail(err)
	}
	return a.exit(do(m, cwd))
}

// exit maps one command's error to the process exit code. The errors
// with a softer story than a bare failure are mapped here, once, so
// every command explains them the same way.
func (a *app) exit(err error) int {
	if table, ok := errors.AsType[*routing.UnreadableError](err); ok {
		report.TableUnreadable(a.stderr, a.errStyle, table)
		return 1
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, lifecycle.ErrDeclined):
		fmt.Fprintln(a.stdout, "Agreement declined; nothing was changed.")
		return 1
	case errors.Is(err, lifecycle.ErrPortOccupied), errors.Is(err, lifecycle.ErrPortSilent), errors.Is(err, lifecycle.ErrProxyUnverified):
		report.ProxyProblem(a.stderr, a.errStyle, err)
		return 1
	default:
		return a.fail(err)
	}
}
