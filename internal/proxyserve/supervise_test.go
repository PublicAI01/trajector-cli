package proxyserve_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
)

// childVersion is the version the spawned serving child announces.
const childVersion = "1.0.0"

func TestMain(m *testing.M) {
	os.Exit(procbin.Main(m, map[string]func(args []string) int{
		"serve": func(args []string) int { return proxytest.ServeProxy(args, childVersion) },
	}))
}

// A serving child that crashes while a replaced proxy is still draining
// is restarted by its watchdog from the binary path, which by then holds
// a newer build, and the port by then belongs to the successor. The
// restarted child must defer to whichever proxy of ours holds the port,
// older or newer than itself, and end the watchdog cleanly rather than
// crash-loop or drain the holder.
func TestSuperviseEndsCleanlyWhenAProxyOfOursHoldsThePortWhateverItsVersion(t *testing.T) {
	for _, holderVersion := range []string{"0.0.1", "9.9.9"} {
		t.Run("holder "+holderVersion, func(t *testing.T) {
			layout := proxytest.SpawnedSandboxLayout(t, t.TempDir())
			addr := freeAddr(t)
			holder := proxytest.New(t, proxytest.WithAddr(addr), proxytest.WithVersion(holderVersion), proxytest.WithLayout(layout))
			holder.AdminToken()

			a := proxytest.Assembly(layout, childVersion, procbin.Self(t, "serve"), addr)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var stdout bytes.Buffer
			if err := proxyserve.Supervise(ctx, a, 0, &stdout, io.Discard); err != nil {
				t.Fatalf("Supervise = %v, want a clean exit beside the holder", err)
			}
			if want := "proxy already running at " + addr + " (version " + holderVersion + ")"; !strings.Contains(stdout.String(), want) {
				t.Errorf("child stdout = %q, want %q", stdout.String(), want)
			}
			if h := holder.Healthz(); h.Version != holderVersion {
				t.Errorf("holder healthz version = %q, want the holder still serving as %s", h.Version, holderVersion)
			}
		})
	}
}
