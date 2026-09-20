package proxytest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// trajectorBehavior is what every process a test spawns runs as: the
// trajector CLI, not the test suite that re-executed the binary.
const trajectorBehavior = "cli"

// runFileEnv names the file a spawned CLI records its arguments and
// exit code in. A detached process is released, never awaited, so what
// it ran is read back from disk.
const runFileEnv = "TRAJECTOR_TEST_RUN_FILE"

// runUnrecordable is the exit code a spawned CLI reports when it
// cannot record its run, so no test reads an earlier record as this
// run's.
const runUnrecordable = 98

// Main runs the package's tests, except in a process a test spawned as
// the executable a device installs: there this binary is the trajector
// CLI, as silent as a detached process is.
func Main(m *testing.M) {
	procbin.Main(m, map[string]func(args []string) int{trajectorBehavior: runTrajector})
}

func runTrajector(args []string) int {
	exit := cli.Run(args, strings.NewReader(""), io.Discard, io.Discard)
	path := os.Getenv(runFileEnv)
	if path == "" {
		return exit
	}
	record := fmt.Sprintf("%d\n%s", exit, strings.Join(args, " "))
	if err := os.WriteFile(path+".tmp", []byte(record), 0o600); err != nil {
		return runUnrecordable
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return runUnrecordable
	}
	return exit
}

// Trajector is the executable a device installs, played by this test
// binary re-executed as the CLI.
type Trajector struct {
	t       *testing.T
	path    string
	runFile string
}

// InstalledTrajector makes this test binary the executable a device
// installs, and homes every process it spawns on its own device under
// home so a spawned CLI never resolves the developer's directories.
// What such a process ran is read back with AwaitRun.
func InstalledTrajector(t *testing.T, home string) *Trajector {
	t.Helper()
	userdirs.Isolate(t.Setenv, home)
	x := &Trajector{t: t, path: procbin.Self(t, trajectorBehavior), runFile: filepath.Join(t.TempDir(), "run")}
	t.Setenv(runFileEnv, x.runFile)
	return x
}

// Path is the executable to install, as a device's ExecPath.
func (x *Trajector) Path() string { return x.path }

// AwaitRun waits for a spawned process to record its run and reports
// the arguments it ran with and the code it exited on.
func (x *Trajector) AwaitRun(within time.Duration) (string, int) {
	x.t.Helper()
	deadline := time.Now().Add(within)
	for {
		record, err := fsatomic.ReadFile(x.runFile)
		if err == nil {
			code, args, ok := strings.Cut(string(record), "\n")
			exit, convErr := strconv.Atoi(code)
			if !ok || convErr != nil {
				x.t.Fatalf("a spawned process recorded its run as %q", record)
			}
			return args, exit
		}
		if time.Now().After(deadline) {
			x.t.Fatal("no process the test spawned recorded a run")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SpawnedDevice is a device a process the test spawns resolves for
// itself: the environment points at these directories, so a spawned
// trajector reads and writes the same stores the test does instead of
// the developer's. Whatever proxy it brings up is drained when the
// test ends.
type SpawnedDevice struct {
	Sandbox   *Sandbox
	Layout    userdirs.Layout
	ExecPath  string
	ProxyAddr string
	proxy     *proxylife.Proxy
}

// SpawnDevice re-homes a device onto one directory, with home kept as
// the user's own, points it at the service a spawned process uploads
// to, and installs this test binary as the executable it spawns.
// version is the build a proxy of this device announces: the drain at
// the end of the test recognizes its own by it.
func SpawnDevice(t *testing.T, home, version, service string) *SpawnedDevice {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	layout := SandboxLayout(t, dir)
	d := &SpawnedDevice{
		Sandbox:   Open(t, layout),
		Layout:    layout,
		ExecPath:  procbin.Self(t, trajectorBehavior),
		ProxyAddr: IdleAddr(t),
	}
	d.Sandbox.pointAtService(service)
	t.Setenv(cli.ProxyAddrEnv, d.ProxyAddr)
	d.proxy = proxylife.For(layout, version, d.ExecPath, d.ProxyAddr, nil)
	t.Cleanup(func() { _ = d.proxy.StopGone() })
	return d
}

// ResidentProcessIsOurs reports whether a healthy proxy of this
// device's build holds the proxy address.
func (d *SpawnedDevice) ResidentProcessIsOurs() bool {
	return d.proxy.Observe().Holder == proxylife.HolderOurs
}
