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

func (a *app) proxyCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "usage: trajector %s %s\n", proxylife.Command, proxylife.Supervise)
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
	addr := fs.String("addr", "", "listen address")
	idle := fs.Duration("idle-timeout", 0, "exit after this much authorized-traffic silence")
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
