package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

func proxyCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "usage: trajector %s %s\n", proxylife.Command, proxylife.Supervise)
		return 2
	}
	switch args[0] {
	case proxylife.Supervise, proxylife.Serve:
		return runProxy(args[0], args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "trajector: unknown proxy command %q\n", args[0])
		return 2
	}
}

// uploadCmd triggers a flush through the machine's one flusher.
func (a *app) uploadCmd(args []string) int {
	force := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--force":
		force = true
	default:
		fmt.Fprintln(a.stderr, "usage: trajector upload [--force]")
		return 2
	}
	m, err := a.machine()
	if err != nil {
		fmt.Fprintf(a.stderr, "trajector: %v\n", err)
		return 1
	}
	if err := m.Upload(force, a.io()); err != nil {
		fmt.Fprintf(a.stderr, "trajector: %v\n", err)
		return 1
	}
	return 0
}

func runProxy(mode string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("proxy "+mode, flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "", "listen address")
	idle := fs.Duration("idle-timeout", 0, "exit after this much authorized-traffic silence")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	m, err := machineAt(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "trajector: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == proxylife.Supervise {
		err = m.SuperviseProxy(ctx, *idle, stdout, stderr)
	} else {
		err = m.ServeProxy(ctx, *idle, stdout, stderr)
	}
	switch {
	case err == nil || errors.Is(err, context.Canceled):
		return 0
	case errors.Is(err, proxylife.ErrPortOccupied):
		fmt.Fprintf(stderr, "trajector: %v\n", err)
		fmt.Fprintln(stderr, "trajector: run `trajector doctor` to identify the process holding the port")
		return 1
	default:
		fmt.Fprintf(stderr, "trajector: %v\n", err)
		return 1
	}
}
