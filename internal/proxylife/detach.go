package proxylife

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// openLogAppend opens logPath for appending, creating its directory
// first; the null device stands in when logPath is empty.
func openLogAppend(logPath string) (*os.File, error) {
	if logPath == "" {
		logPath = os.DevNull
	} else if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// StartDetached starts path with args in its own session or process
// group so it survives the caller's exit. Stdout and stderr are appended
// to logPath (the null device when empty); stdin is the null device. The
// child is released, not awaited.
func StartDetached(path string, args []string, logPath string) (pid int, err error) {
	logFile, err := openLogAppend(logPath)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()

	cmd := exec.Command(path, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid = cmd.Process.Pid
	cmd.Process.Release()
	return pid, nil
}

// Starter starts a process this binary is asked to leave running —
// the proxy, and the one-shot reader a session hook hands its work to
// — given the same arguments StartDetached takes, and reports its pid.
// StartDetached is the one production uses. A starter that
// deliberately starts no process reports the pid noProcess, and the
// caller then has nothing to wait for.
type Starter func(path string, args []string, logPath string) (pid int, err error)

// OrDetached reports s, or the detached spawn when s is nil. A caller
// handed no starter reaches production's default through this, so "no
// starter means detach" is stated in one place.
func (s Starter) OrDetached() Starter {
	if s == nil {
		return StartDetached
	}
	return s
}

// noProcess is the pid a starter reports when it started nothing.
const noProcess = 0

// recordedStart marks a line a recording starter wrote. The command
// and its target follow it, separated by one space; the target may
// hold spaces of its own, the command may not.
const recordedStart = "would have started "

// LoggedStart is one start a recording starter wrote down instead of
// performing: what the argv asked this binary to run, and the address
// or directory that argv named.
type LoggedStart struct {
	Command string
	Target  string
}

// RecordStartsIn returns a Starter that starts nothing: it appends
// what it was asked to start to logPath and reports no process. A test
// suite that drives the CLI in its own process installs it, so a
// session hook still reaches the start — and can be asserted to have
// reached it — without leaving behind a process that outlives the
// suite. Every start goes to the one file named here whatever log the
// caller asks for, because a caller that wants no log of its own must
// stay observable all the same.
func RecordStartsIn(logPath string) Starter {
	return func(_ string, args []string, _ string) (int, error) {
		f, err := openLogAppend(logPath)
		if err != nil {
			return noProcess, err
		}
		defer f.Close()
		start := startIn(args)
		fmt.Fprintf(f, "%s %s%s %s\n", time.Now().UTC().Format(time.RFC3339), recordedStart, start.Command, start.Target)
		return noProcess, nil
	}
}

// LoggedStarts reads back, in order, every start a recording starter
// appended to logPath. A log that was never written holds none. The
// line is this package's own, so no caller has to spell its shape.
func LoggedStarts(logPath string) ([]LoggedStart, error) {
	data, err := fsatomic.ReadFile(logPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var starts []LoggedStart
	for line := range strings.SplitSeq(string(data), "\n") {
		_, rest, ok := strings.Cut(line, recordedStart)
		if !ok {
			continue
		}
		command, target, ok := strings.Cut(rest, " ")
		if !ok {
			continue
		}
		starts = append(starts, LoggedStart{Command: command, Target: target})
	}
	return starts, nil
}

// startIn reports what an argv asks for: its first word, and the
// listen address that follows the address flag — or, when the argv
// carries no address, its last word.
func startIn(args []string) LoggedStart {
	if len(args) == 0 {
		return LoggedStart{}
	}
	target := args[len(args)-1]
	if i := slices.Index(args, addrFlag); i >= 0 && i+1 < len(args) {
		target = args[i+1]
	}
	return LoggedStart{Command: args[0], Target: target}
}
