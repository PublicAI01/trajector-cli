package cli_test

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// TestMain lets a spawned process be this binary's CLI entry point, so
// a process tree started by one command prints exactly what production
// prints.
func TestMain(m *testing.M) {
	procbin.Main(m, map[string]func(args []string) int{
		"cli": func(args []string) int {
			return cli.Run(args, strings.NewReader(""), os.Stdout, os.Stderr)
		},
	})
}

// Two sessions starting a proxy at once is the ordinary race, and the
// loser's report is what the user reads in the proxy log. Whoever loses
// the bind must be told a sibling won it — never that a stranger holds
// the port, and never to go stop a process that is trajector's own.
func TestConcurrentStartsConvergeWithoutBlamingAForeignProcess(t *testing.T) {
	e := clitest.New(t)
	layout := e.Layout()
	addr := freeLoopbackAddr(t)
	exe := procbin.Self(t, "cli")
	first := proxylife.For(layout, "dev", exe, addr)
	second := proxylife.For(layout, "dev", exe, addr)
	t.Cleanup(func() {
		// A sibling still inside its startup grace when the winner
		// drains exits on its bind error and is restarted by its
		// supervisor onto the now-free port — the self-healing working
		// as designed — so one drain is not always the last. Drain
		// until both trees are gone: the port coming free says the
		// serving child exited, the log becoming removable says no
		// process holds its append handle anymore, which is the release
		// Windows' delete refusal measures.
		deadline := time.Now().Add(30 * time.Second)
		for {
			first.Stop()
			if portFree(addr) && logRemoved(layout) {
				return
			}
			if time.Now().After(deadline) {
				t.Error("spawned proxies kept the port or the log through repeated drains")
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	})

	done := make(chan error, 2)
	for _, p := range []*proxylife.Proxy{first, second} {
		go func() { done <- p.Ensure() }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent Ensure: %v", err)
		}
	}

	if v := first.Observe(); v.Holder != proxylife.HolderOurs {
		t.Fatalf("holder = %v after concurrent starts, want ours", v.Holder)
	}
	log := proxyLogContents(t, layout)
	if strings.Contains(log, proxylife.ErrPortOccupied.Error()) {
		t.Errorf("the proxy log blames a foreign process for a sibling's win:\n%s", log)
	}
	if strings.Contains(log, report.ProxyRemedy(proxylife.ErrPortOccupied)) {
		t.Errorf("the proxy log advises stopping a process that is a sibling proxy:\n%s", log)
	}
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// portFree reports whether nothing accepts connections at addr.
func portFree(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err != nil {
		return true
	}
	conn.Close()
	return false
}

// logRemoved removes the proxy log, reporting whether it is gone — on
// Windows the remove is refused while any process holds the file open.
func logRemoved(layout userdirs.Layout) bool {
	err := os.Remove(layout.ProxyLog())
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

func proxyLogContents(t *testing.T, layout userdirs.Layout) string {
	t.Helper()
	data, err := os.ReadFile(layout.ProxyLog())
	if err != nil {
		t.Fatalf("reading the proxy log: %v", err)
	}
	return string(data)
}
