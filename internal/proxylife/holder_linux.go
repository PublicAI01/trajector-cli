package proxylife

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HolderOf reads what the operating system says about the process
// listening on addr, and reports false when this device cannot tell.
// On Linux it is two reads of /proc and no command is run: the
// listening socket is found by port, and the process is the one
// holding that socket. A socket held by another user's process is not
// attributable from here, which answers false.
func HolderOf(addr string) (Process, bool) {
	inodes := listeningInodes(addr)
	if len(inodes) == 0 {
		return Process{}, false
	}
	pid, ok := pidHolding(inodes)
	if !ok {
		return Process{}, false
	}
	return describe(pid), true
}

// listeningInodes are the socket inodes of every listening socket on
// addr's port, over both IP versions. The port alone selects them: a
// holder bound to the wildcard address holds the loopback address
// too, and the caller has already found that something answers there.
func listeningInodes(addr string) []string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return nil
	}
	suffix := fmt.Sprintf(":%04X", number)
	var inodes []string
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			// local address, state, and the inode, in the columns the
			// table has carried since it existed.
			const (
				local = 1
				state = 3
				inode = 9
			)
			if len(fields) <= inode || fields[state] != "0A" || !strings.HasSuffix(fields[local], suffix) {
				continue
			}
			inodes = append(inodes, fields[inode])
		}
	}
	return inodes
}

// pidHolding is the process holding one of the sockets, found by
// looking at what each process has open. Processes this one may not
// look into are skipped: not being able to see is not an answer.
func pidHolding(inodes []string) (int, bool) {
	want := make(map[string]bool, len(inodes))
	for _, inode := range inodes {
		want["socket:["+inode+"]"] = true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", entry.Name(), "fd", fd.Name()))
			if err == nil && want[link] {
				return pid, true
			}
		}
	}
	return 0, false
}

// holderCommand is what a user runs to read the holder for
// themselves. It is not what HolderOf reads: a command belongs to the
// user's own shell, where ss is part of a Linux system and lsof often
// is not installed at all.
func holderCommand(port string) string { return "ss -ltnp 'sport = :" + port + "'" }

// describe reads what the process says about itself. A field that
// cannot be read is left empty rather than guessed at.
func describe(pid int) Process {
	proc := Process{PID: pid}
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	if name, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		proc.Name = strings.TrimSpace(string(name))
	}
	if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		// A program file replaced since the process started is marked
		// by the kernel. The path no longer names the running program,
		// so it is not kept.
		if !strings.HasSuffix(exe, " (deleted)") {
			proc.Exe = exe
		}
	}
	if argv, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		for _, arg := range strings.Split(string(argv), "\x00") {
			if arg != "" {
				proc.Argv = append(proc.Argv, arg)
			}
		}
	}
	return proc
}
