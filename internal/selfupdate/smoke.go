package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"time"
)

// smokeTimeout bounds the one run of a staged binary before it is
// installed. `version` prints a line and exits without reading any
// state, so a binary that has not answered by then is not going to.
const smokeTimeout = 5 * time.Second

// smokeWaitDelay is how long the run is given, once it has exited or
// been killed, for whatever it started to let go of its output.
const smokeWaitDelay = time.Second

// smokeOutputLimit is how much of what the staged binary prints is
// kept. The answer wanted is one short line; anything longer is wrong
// already, and only its start is worth quoting.
const smokeOutputLimit = 256

// smoke runs the staged binary's `version` and checks that it answers
// exactly as the release it was published as. That catches what a
// checksum cannot: a build for another platform or architecture, a
// truncated file, a missing shared library, and an archive that holds
// some other version under the right name. It returns an
// *UnusableError for a binary that fails, and ctx's error when the
// run was cut short by the caller rather than by the binary.
func smoke(ctx context.Context, staged, version string) error {
	runCtx, cancel := context.WithTimeout(ctx, smokeTimeout)
	defer cancel()
	out := &cappedBuffer{limit: smokeOutputLimit}
	cmd := exec.CommandContext(runCtx, staged, "version")
	cmd.Stdout = out
	cmd.WaitDelay = smokeWaitDelay
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	reason := ""
	_, exited := errors.AsType[*exec.ExitError](err)
	switch {
	case runCtx.Err() != nil:
		reason = fmt.Sprintf("it did not answer within %s", smokeTimeout)
	case exited:
		reason = "it exited with " + err.Error()
	case err != nil:
		// A start that failed is reported by the system against the
		// staged file's name, which is gone by the time anyone reads
		// the reason; what the system said is the part worth keeping.
		if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
			err = pathErr.Err
		}
		reason = "it cannot be run: " + err.Error()
	case strings.TrimSuffix(out.String(), "\n") != "trajector "+version:
		reason = fmt.Sprintf("it reports itself as %q", firstLine(out.String()))
	default:
		return nil
	}
	return &UnusableError{Version: version, Reason: reason}
}

// firstLine is the start of what a binary printed, for quoting in a
// one-line reason.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// cappedBuffer keeps the first limit bytes written to it and discards
// the rest while still accepting them, so a binary that prints without
// end is neither stored whole nor blocked on a full pipe.
type cappedBuffer struct {
	limit int
	buf   []byte
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }
