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

	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
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

// uploadCmd triggers a flush through the proxy — the machine's one
// flusher — starting it if nothing is listening.
func uploadCmd(args []string, stdout, stderr io.Writer) int {
	force := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--force":
		force = true
	default:
		fmt.Fprintln(stderr, "usage: trajector upload [--force]")
		return 2
	}
	env, err := resolveEnv()
	if err != nil {
		fmt.Fprintf(stderr, "trajector: %v\n", err)
		return 1
	}
	proxy := proxylife.For(env.layout, version, env.execPath, env.proxyAddr)
	if err := proxy.Ensure(); err != nil {
		fmt.Fprintf(stderr, "trajector: %v\n", err)
		return 1
	}
	reply, err := proxy.Flush(force)
	if err != nil {
		fmt.Fprintf(stderr, "trajector: %v\n", err)
		return 1
	}
	if reply.Error != "" {
		if reply.Batches > 0 {
			fmt.Fprintf(stdout, "Uploaded %d batch(es), %d rawcall(s) before failing.\n", reply.Batches, reply.Records)
		}
		fmt.Fprintf(stderr, "trajector: %s\n", reply.Error)
		return 1
	}
	switch upload.Outcome(reply.Outcome) {
	case upload.Uploaded:
		fmt.Fprintf(stdout, "Uploaded %d batch(es), %d rawcall(s).\n", reply.Batches, reply.Records)
	case upload.Empty:
		fmt.Fprintln(stdout, "Nothing to upload.")
	case upload.BelowThreshold:
		fmt.Fprintln(stdout, "Below the upload thresholds; use --force to upload anyway.")
	case upload.Paused:
		fmt.Fprintln(stdout, "Not signed in; run `trajector login` first. Captured data is kept.")
	case upload.UpgradeRequired:
		if v := upload.LoadHandshake(env.layout.UploadDir()).MinClientVersion; v != "" {
			fmt.Fprintf(stdout, "Uploads are paused: the service requires trajector %s or newer (this is %s).\n", v, version)
		} else {
			fmt.Fprintln(stdout, "Uploads are paused: the service requires a newer trajector version.")
		}
		fmt.Fprintln(stdout, "Captured data is kept. Upgrade trajector to resume, or retry with --force.")
	case upload.Deferred:
		fmt.Fprintln(stdout, "The service asked to slow down; uploads resume automatically. Use --force to try now.")
	default:
		fmt.Fprintf(stdout, "Flush finished: %s\n", reply.Outcome)
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

	env, err := resolveEnv()
	if err != nil {
		fmt.Fprintf(stderr, "trajector: %v\n", err)
		return 1
	}
	if *addr != "" {
		env.proxyAddr = *addr
	}
	proxy := proxylife.For(env.layout, version, env.execPath, env.proxyAddr)
	proxy.Uploads(platform.New(env.platformURL, version), tokenstore.Open(env.layout.SecretsDir()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == proxylife.Supervise {
		err = proxy.Supervise(ctx, *idle, stdout, stderr)
	} else {
		err = proxy.Run(ctx, *idle, stdout, stderr)
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
