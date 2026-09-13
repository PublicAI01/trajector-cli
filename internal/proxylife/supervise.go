package proxylife

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Restart pacing for the supervised child. More than maxCrashes crashes
// within crashWindow means the child cannot hold the port and restarting
// would only loop.
const (
	restartDelay = 500 * time.Millisecond
	crashWindow  = time.Minute
	maxCrashes   = 5
)

// shutdownGrace bounds how long a cancelled child may take to shut
// itself down before the watchdog kills it. It covers the child's own
// budgets — the drain of in-flight requests and the uploader's final
// flush — with room to spare, since exceeding it costs exactly what
// sending SIGKILL immediately used to cost.
const shutdownGrace = 45 * time.Second

// terminateSignal is what a cancelled child is asked to stop with.
// syscall.SIGTERM is defined on every platform Go builds for; sending
// it is what fails on Windows, and superviseChild falls back there.
const terminateSignal = syscall.SIGTERM

type superviseConfig struct {
	// Command is the child argv; Command[0] is the binary path.
	Command []string
	// Stdout and Stderr receive the child's output.
	Stdout, Stderr io.Writer
	Logf           func(format string, args ...any)
}

// superviseChild is a minimal watchdog: start the child, restart it when
// it crashes, and go away when the child ends its own life cleanly — the
// child's idle exit is the supervisor's exit.
func superviseChild(ctx context.Context, cfg superviseConfig) error {
	if len(cfg.Command) == 0 {
		return fmt.Errorf("proxylife: empty command")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}

	var crashes []time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
		cmd.Stdout = cfg.Stdout
		cmd.Stderr = cfg.Stderr
		// CommandContext's default cancellation is Process.Kill — SIGKILL,
		// which the child cannot catch. The child is where every graceful
		// exit guarantee lives: the uploader's final flush, the drain of
		// in-flight requests, and the drain of finished-but-unwritten
		// captures still sitting in the record queue. Killing it outright
		// loses all three, so an ordinary reboot or logout — which TERMs
		// this watchdog — cut live Claude Code sessions off and dropped
		// whole captures that were already complete. Ask first, and keep
		// the kill only as the backstop for a child that will not go.
		// 2026-09-13.
		cmd.Cancel = func() error {
			if err := cmd.Process.Signal(terminateSignal); err != nil {
				// Windows has no SIGTERM to send; the kill is all there is.
				return cmd.Process.Kill()
			}
			return nil
		}
		cmd.WaitDelay = shutdownGrace
		err := cmd.Run()
		if ctx.Err() != nil {
			// Cancelled: the child stopped because it was asked to, so its
			// exit status is not a verdict on the proxy. Checked before the
			// clean-exit arm because a child that now shuts down gracefully
			// exits zero, and that must still read as cancellation rather
			// than as the child ending its own life.
			return ctx.Err()
		}
		if err == nil {
			return nil
		}

		now := time.Now()
		recent := crashes[:0]
		for _, c := range crashes {
			if now.Sub(c) < crashWindow {
				recent = append(recent, c)
			}
		}
		crashes = append(recent, now)
		if len(crashes) > maxCrashes {
			return fmt.Errorf("proxylife: %d crashes within %s, giving up: %w", len(crashes), crashWindow, err)
		}
		cfg.Logf("proxy exited abnormally (%v); restarting", err)
		select {
		case <-time.After(restartDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
