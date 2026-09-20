package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

// proxyUsage names the mode a user starts by hand. The serve mode is
// dispatched too, but only the supervisor starts it.
const proxyUsage = "usage: trajector " + proxylife.Command + " " + proxylife.Supervise

// Flag names of the proxy modes, spelled once for the pre-parse and for
// the flag set that reads their values.
const (
	addrFlag = "addr"
	idleFlag = "idle-timeout"
)

// proxyFlags is every spelling the flag package accepts for the flags
// the proxy modes take, so the pre-parse lets them through to it.
var proxyFlags = []string{"-" + addrFlag, "--" + addrFlag, "-" + idleFlag, "--" + idleFlag}

func (a *app) proxyCmd(args []string) int {
	if code, answered := a.preparse(proxyUsage, args, proxyFlags); answered {
		return code
	}
	if len(args) == 0 {
		fmt.Fprintln(a.stderr, proxyUsage)
		return 2
	}
	switch args[0] {
	case proxylife.Supervise, proxylife.Serve:
		return a.runProxy(args[0], args[1:])
	default:
		fmt.Fprintf(a.stderr, "trajector: unknown proxy command %q\n", args[0])
		return 2
	}
}

// uploadCmd triggers a flush through the machine's one flusher.
func (a *app) uploadCmd(args []string) int {
	args, force := takeFlag(args, "--force")
	return a.with("usage: trajector upload [--force]", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Upload(force, a.io())
	})
}

func (a *app) runProxy(mode string, args []string) int {
	fs := flag.NewFlagSet("proxy "+mode, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	addr := fs.String(addrFlag, "", "listen address, matched by its literal spelling: probes, admin-token files, and the challenge all treat localhost and 127.0.0.1 as different addresses, so every command must spell it the way serve did")
	idle := fs.Duration(idleFlag, 0, "exit after this much authorized-traffic silence")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	m, err := machineAt(*addr)
	if err != nil {
		return a.fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == proxylife.Supervise {
		err = m.SuperviseProxy(ctx, *idle, a.stdout, a.stderr)
	} else {
		err = m.ServeProxy(ctx, *idle, a.stdout, a.stderr)
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	return a.exit(err)
}
