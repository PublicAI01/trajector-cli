package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
)

// verifyTimeout bounds the new build's check of itself. Starting a
// proxy, asking it for its health and stopping it takes well under a
// second; a build that has not answered by then is not going to, and
// it is stopped and refused like one that answered no.
const verifyTimeout = 20 * time.Second

// finishTimeout bounds the new build's finish of its install. The
// install is committed by then, so running out of it costs nothing but
// a finish the new build runs again later.
const finishTimeout = 30 * time.Second

// verify runs the build just put at execPath to have it accept the
// install: it starts a proxy of its own, on directories and a port of
// its own, and says whether that proxy served. It returns an
// *UnusableError for a build that did not, and ctx's error when the run
// was cut short by the caller rather than by the build.
//
// execPath is the path this upgrade was given, never one read back
// from the running process: once the binary has been replaced, the
// system may name the running image's file as deleted.
func verify(ctx context.Context, execPath, version string) error {
	runCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	said := &cappedBuffer{limit: smokeOutputLimit}
	cmd := exec.CommandContext(runCtx, execPath, claudesettings.HookArgv(claudesettings.HookUpgradeVerify)...) //nolint:gosec // G204: the installation's own binary, at the path resolved before the install, with constant arguments
	cmd.Stderr = said
	cmd.WaitDelay = smokeWaitDelay
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	reason := ""
	_, exited := errors.AsType[*exec.ExitError](err)
	switch {
	case runCtx.Err() != nil:
		reason = fmt.Sprintf("its proxy did not start within %s", verifyTimeout)
	case exited:
		// The new build says why on its error stream, under the prefix
		// every trajector error carries; the prefix says nothing here.
		why := strings.TrimPrefix(firstLine(said.String()), "trajector: ")
		if why == "" {
			why = "it exited with " + err.Error()
		}
		reason = "its proxy does not start here: " + why
	case err != nil:
		if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
			err = pathErr.Err
		}
		reason = "it cannot be run: " + err.Error()
	default:
		return nil
	}
	return &UnusableError{Version: version, Reason: reason}
}

// finish runs the build at execPath, whose install was just committed,
// for the steps only it knows how to take. Its error is whatever kept
// the build from saying it finished.
func finish(ctx context.Context, execPath string) error {
	runCtx, cancel := context.WithTimeout(ctx, finishTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, execPath, claudesettings.HookArgv(claudesettings.HookUpgradeFinish)...) //nolint:gosec // G204: the installation's own binary, at the path resolved before the install, with constant arguments
	cmd.WaitDelay = smokeWaitDelay
	return cmd.Run()
}
