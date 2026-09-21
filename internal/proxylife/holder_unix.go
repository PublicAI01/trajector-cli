//go:build !linux && !windows

package proxylife

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// holderTimeout bounds each reading of the holder. The reading is part
// of a diagnosis a user waits on, so it is given a few seconds and
// then abandoned: not knowing who holds the port is an answer a
// surface can state, a command that never returns is not.
const holderTimeout = 2 * time.Second

// HolderOf reads what the operating system says about the process
// listening on addr, and reports false when this device cannot tell.
// Where no file system names the holder, the system's own tools are
// asked: one for which process holds the port, one for how it was
// started. A tool that is absent, refuses, or says nothing answers
// the same way as a holder that cannot be attributed — nothing was
// read.
func HolderOf(addr string) (Process, bool) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Process{}, false
	}
	ask := holderQuery(port)
	out, err := run(ask[0], append(ask[1:], "-Fpc")...)
	if err != nil {
		return Process{}, false
	}
	pid, name := 0, ""
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) < 2 {
			continue
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			if id, err := strconv.Atoi(value); err == nil && pid == 0 {
				pid = id
			}
		case 'c':
			if name == "" {
				name = value
			}
		}
	}
	if pid == 0 {
		return Process{}, false
	}
	proc := describe(pid)
	// The name is the one the port reading gave: it is what the
	// operating system calls the process, which the reading by pid asks
	// nothing about.
	proc.Name = name
	return proc, true
}

// holderQuery is the question that names the process listening on
// port. The reading adds the field output the parser above needs; what
// a user runs is the same question without it.
func holderQuery(port string) []string {
	return []string{"lsof", "-nP", "-iTCP:" + port, "-sTCP:LISTEN"}
}

// holderCommand is what a user runs to read the holder for themselves,
// and is the same question this file asks.
func holderCommand(port string) string { return strings.Join(holderQuery(port), " ") }

// describe reads what one process says about itself, by pid alone. A
// field that cannot be read is left empty rather than guessed at, and
// the name is not asked for here — the tool that finds the holder by
// port is the one that reports it.
func describe(pid int) Process {
	proc := Process{PID: pid}
	id := strconv.Itoa(pid)
	if exe, err := run("ps", "-o", "comm=", "-p", id); err == nil {
		// Some of these systems report a name, and a truncated one at
		// that, where others report the path. A name names no program
		// file, so it is not kept.
		if path := strings.TrimSpace(exe); filepath.IsAbs(path) {
			proc.Exe = path
		}
	}
	if argv, err := run("ps", "-o", "args=", "-p", id); err == nil {
		proc.Argv = strings.Fields(argv)
	}
	return proc
}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), holderTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
