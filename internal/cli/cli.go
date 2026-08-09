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
	"time"

	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// version is stamped at build time by the release pipeline.
var version = "dev"

// ProxyAddrEnv overrides the proxy address, for tests and unusual
// setups. Production always uses the fixed address.
const ProxyAddrEnv = "TRAJECTOR_PROXY_ADDR"

// NowEnv pins the machine's clock to a fixed RFC3339 instant, for
// tests. Production leaves it unset.
const NowEnv = "TRAJECTOR_NOW"

type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// Run executes the CLI and returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version":
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
  version      print the trajector version
  proxy run    run the local proxy (internal; started automatically)
  hook         session hook entry points (internal; injected by enable)
`)
}

// runtimeEnv is everything a command needs to know about the machine
// it runs on, resolved in one place.
type runtimeEnv struct {
	layout      userdirs.Layout
	home        string
	execPath    string
	proxyAddr   string
	platformURL string
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
		platformURL: platform.DefaultBaseURL,
	}
	if addr := os.Getenv(ProxyAddrEnv); addr != "" {
		env.proxyAddr = addr
	}
	platformURL, err := configuredPlatformURL(layout.ConfigFile())
	if err != nil {
		return runtimeEnv{}, err
	}
	if platformURL != "" {
		env.platformURL = platformURL
	}
	return env, nil
}

// configuredPlatformURL reads the service endpoint override from the
// user config file. The endpoint decides where captured data and the
// device token go, so it is never read from an environment variable: a
// repository's committed settings reach this process's environment
// through the session hooks, and must not be able to choose the
// destination. An absent file means no override; an unreadable one
// fails the command loudly rather than silently uploading elsewhere.
func configuredPlatformURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var cfg struct {
		PlatformURL string `json:"platform_url"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return cfg.PlatformURL, nil
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
	deps := lifecycle.Deps{
		Layout:    env.layout,
		Tokens:    tokenstore.Open(env.layout.SecretsDir()),
		Platform:  platform.New(env.platformURL, version),
		Version:   version,
		ExecPath:  env.execPath,
		ProxyAddr: env.proxyAddr,
		Home:      env.home,
		Getenv:    os.Getenv,
	}
	if v := os.Getenv(NowEnv); v != "" {
		at, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", NowEnv, err)
		}
		deps.Now = func() time.Time { return at }
	}
	return lifecycle.Open(deps)
}

// io hands the machine this invocation's streams.
func (a *app) io() lifecycle.IO {
	return lifecycle.IO{In: a.stdin, Out: a.stdout, Err: a.stderr}
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

// with is the shape every command shares: the argument-count check
// against usage, the prelude, and the one mapping from the machine's
// answer to an exit code.
func (a *app) with(usage string, args []string, nargs int, do func(m *lifecycle.Machine, cwd string) error) int {
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
	switch {
	case err == nil:
		return 0
	case errors.Is(err, lifecycle.ErrDeclined):
		fmt.Fprintln(a.stdout, "Agreement declined; nothing was changed.")
		return 1
	case errors.Is(err, lifecycle.ErrPortOccupied), errors.Is(err, lifecycle.ErrProxyUnverified):
		fmt.Fprintf(a.stderr, "trajector: WARNING: %v\n", err)
		if remedy := lifecycle.ProxyRemedy(err); remedy != "" {
			fmt.Fprintf(a.stderr, "trajector: %s\n", remedy)
		}
		return 1
	default:
		return a.fail(err)
	}
}
