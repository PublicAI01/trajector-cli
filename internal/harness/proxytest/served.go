package proxytest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// unroutableService is a service address that nothing answers on.
const unroutableService = "http://127.0.0.1:1"

// spawnedProxyIdle is how long a proxy a test spawns may forward
// nothing before it exits on its own.
const spawnedProxyIdle = 30 * time.Second

// Assembly is what a serving process on layout is built from, with
// every store opened on layout as a device opens its own, announcing
// version. Its uploader never reaches a network; a test that uploads
// sets Service.
func Assembly(layout userdirs.Layout, version, execPath, addr string) proxyserve.Assembly {
	return proxyserve.Assembly{
		Layout:   layout,
		Tokens:   tokenstore.Files(layout.SecretsDir()),
		Service:  platform.New(unroutableService, version),
		Consent:  consent.Open(layout.ConsentFile()),
		Routes:   routing.OpenStore(layout.RoutingTable()),
		Registry: follow.Open(layout.FollowDir()),
		Version:  version,
		ExecPath: execPath,
		Addr:     addr,
	}
}

// ServeProxy is a proxy process a test spawns, and returns its exit
// code. args is the argv proxylife builds for one; the process runs the
// watchdog or the serving proxy that args names, as version, on the
// directories its environment names, through Assembly.
func ServeProxy(args []string, version string) int {
	if len(args) < 4 || args[0] != proxylife.Command {
		return 96
	}
	layout, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		return 95
	}
	exe, err := os.Executable()
	if err != nil {
		return 95
	}
	a := Assembly(layout, version, exe, args[3])
	ctx := context.Background()
	if args[1] == proxylife.Supervise {
		err = proxyserve.Supervise(ctx, a, spawnedProxyIdle, os.Stdout, os.Stderr)
	} else {
		err = proxyserve.Serve(ctx, a, spawnedProxyIdle, os.Stdout, os.Stderr)
	}
	if err != nil {
		return 1
	}
	return 0
}

// SpawnedSandboxLayout points every process the test spawns at the
// directories SandboxLayout puts inside dir, and returns that layout:
// a spawned process resolves its directories from its environment.
func SpawnedSandboxLayout(t *testing.T, dir string) userdirs.Layout {
	t.Helper()
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, dir)
	}
	return SandboxLayout(t, dir)
}
