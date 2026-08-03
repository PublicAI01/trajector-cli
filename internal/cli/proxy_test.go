package cli_test

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestProxyServeYieldsToHealthyInstance(t *testing.T) {
	e := clitest.New(t)
	addr := proxytest.New(t).Addr()

	got := e.Run("proxy", "serve", "--addr", addr)
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q), a concurrent-start loser must defer with 0", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "already running") {
		t.Errorf("stdout = %q", got.Stdout)
	}
}

func TestProxyServeRefusesForeignPortHolder(t *testing.T) {
	e := clitest.New(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go http.Serve(l, http.NotFoundHandler())

	got := e.Run("proxy", "serve", "--addr", l.Addr().String())
	if got.Exit != 1 {
		t.Fatalf("exit = %d, want a loud failure", got.Exit)
	}
	if !strings.Contains(got.Stderr, "not the trajector proxy") {
		t.Errorf("stderr = %q", got.Stderr)
	}
}

func TestProxyCommandUsage(t *testing.T) {
	e := clitest.New(t)
	if got := e.Run("proxy"); got.Exit != 2 {
		t.Errorf("bare proxy exit = %d, want 2", got.Exit)
	}
	if got := e.Run("proxy", "frobnicate"); got.Exit != 2 || !strings.Contains(got.Stderr, "unknown proxy command") {
		t.Errorf("unknown subcommand = %+v", got)
	}
}
