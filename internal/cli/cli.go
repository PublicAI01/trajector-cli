// Package cli implements the trajector command-line interface. It owns
// argv, exit codes, and what the user reads; everything a command
// actually does belongs to the lifecycle machine. Run is driven
// in-process by tests, so command implementations must write only to
// the provided streams, read only the provided stdin, and read
// configuration through the environment.
package cli

import (
	"fmt"
	"io"
	"os"

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
	case "upload":
		return uploadCmd(args[1:], stdout, stderr)
	case "hook":
		return a.hookCmd(args[1:])
	case proxylife.Command:
		return proxyCmd(args[1:], stdout, stderr)
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
  uninstall    remove every injection and optionally local data
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
	if url := os.Getenv(platform.BaseURLEnv); url != "" {
		env.platformURL = url
	}
	return env, nil
}

// machine assembles the lifecycle machine for this invocation.
func (a *app) machine() (*lifecycle.Machine, error) {
	env, err := resolveEnv()
	if err != nil {
		return nil, err
	}
	return lifecycle.Open(lifecycle.Deps{
		Layout:    env.layout,
		Tokens:    tokenstore.Open(env.layout.SecretsDir()),
		Platform:  platform.New(env.platformURL, version),
		Version:   version,
		ExecPath:  env.execPath,
		ProxyAddr: env.proxyAddr,
		Home:      env.home,
		Getenv:    os.Getenv,
	})
}

// io hands the machine this invocation's streams.
func (a *app) io() lifecycle.IO {
	return lifecycle.IO{In: a.stdin, Out: a.stdout, Err: a.stderr}
}

func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "trajector: %v\n", err)
	return 1
}
