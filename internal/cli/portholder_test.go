package cli_test

import (
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

// A holder that answers nothing costs the user the port exactly as one
// that answers without proof does, so a command that needs the proxy
// says the same things about either: what was observed, what it costs,
// the command that names the holder, and what ends it. A command that
// only reports on the device is not among these: status and doctor
// carry the same words through their own surfaces.
func TestEveryCommandNeedingTheProxyExplainsASilentPortHolder(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*clitest.Env) clitest.Result
	}{
		{
			name: "upload",
			run:  func(e *clitest.Env) clitest.Result { return e.Run("upload", "--force") },
		},
		{
			name: "a session hook",
			run:  func(e *clitest.Env) clitest.Result { return e.InProject("hook", "ensure-proxy") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := clitest.New(t)
			t.Setenv(cli.ProxyAddrEnv, silentPortHolder(t))

			got := tc.run(e)

			if got.Exit != 1 {
				t.Fatalf("exit = %d (stderr: %q), want a loud failure", got.Exit, got.Stderr)
			}
			for _, want := range []string{
				"did not answer a probe",
				"another process holds the proxy port",
				"free the port by stopping whatever holds it",
				"To find the holder: ",
				"Once the port is free, run `trajector doctor`.",
			} {
				if !strings.Contains(got.Stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", got.Stderr, want)
				}
			}
			if strings.Contains(got.Stderr, "could not start the capture proxy") {
				t.Errorf("stderr = %q, want the verdict's own words and no sentence wrapped around them", got.Stderr)
			}
		})
	}
}

// silentPortHolder squats a loopback address and answers nothing: it
// accepts the connection and leaves it open, which is what a port
// forwarded elsewhere and a process that stopped reading both do. The
// listener is this test's own process, which no command may take for a
// proxy of this build's: proof of that needs the proxy's serve command
// on the holder's command line, and a test binary carries none.
func silentPortHolder(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			conn.Close()
		}
	})
	return l.Addr().String()
}
