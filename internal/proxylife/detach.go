package proxylife

import (
	"os"
	"os/exec"
	"path/filepath"
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

// startDetached starts path with args in its own session or process
// group so it survives the caller's exit. Stdout and stderr are appended
// to logPath (the null device when empty); stdin is the null device. The
// child is released, not awaited.
func startDetached(path string, args []string, logPath string) (pid int, err error) {
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
