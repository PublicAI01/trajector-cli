package proxylife

import (
	"net"
	"os"
	"path/filepath"
	"slices"
)

// Process is what this device could read about the process holding an
// address: its process id, what the operating system calls it, the
// program file it runs, and the arguments it was started with. Every
// field is read from the operating system; nothing here is inferred,
// and a surface that prints it says what was observed rather than what
// the program is.
//
// Reading it is best effort by design. A process of another user, a
// platform this build has no reading for, and a listener this device
// cannot attribute all answer the same way: nothing was read.
type Process struct {
	PID  int
	Name string
	// Exe is the absolute path of the program file the process runs,
	// empty when it could not be read. A name that is not a path is no
	// program file: it names whatever the reader's own working
	// directory makes of it, so a reading that yields one leaves this
	// empty.
	Exe string
	// Argv is the command line the process was started with, empty
	// when it could not be read.
	Argv []string
}

// IsProxyOf reports that this process is the proxy this build starts
// itself: the same program file, running the proxy's serve command.
// Anything less is not proof — a name, a port, and a command line are
// each things another program may carry — and an unproven holder is
// never signalled. A program file named by anything but a path proves
// nothing either: what it is would be decided by the working directory
// of whoever reads it.
func (pr Process) IsProxyOf(execPath string) bool {
	if pr.PID <= 0 || !filepath.IsAbs(pr.Exe) || execPath == "" {
		return false
	}
	if !sameFile(pr.Exe, execPath) {
		return false
	}
	return slices.Contains(pr.Argv, Command) && slices.Contains(pr.Argv, Serve)
}

// HolderCommand is the command that names the process holding addr on
// this platform. It is for a surface to print and never for this build
// to run: the user asks their own machine who holds the port, and the
// sentence that introduces the command belongs to whoever prints it.
func HolderCommand(addr string) string { return holderCommand(portOf(addr)) }

// portOf is the port addr names, or addr itself where it names no
// host: a caller that holds only a port may ask with it.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return addr
}

// sameFile reports that two paths name one program file. The
// comparison is the file system's, so a path reached through a
// symbolic link or a different spelling still answers true, and a
// program file that was replaced since the process started answers
// false — which is the safe answer for a decision to signal.
func sameFile(a, b string) bool {
	first, err := os.Stat(a)
	if err != nil {
		return false
	}
	second, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(first, second)
}

// terminate asks the process to stop the way the proxy's own shutdown
// is driven: a signal it catches, never a kill. A process that is
// already gone is the goal state.
func terminate(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(terminateSignal)
}
