package proxylife

import (
	"context"
	"fmt"
	"io"
	"os/exec"
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
		err := cmd.Run()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
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
