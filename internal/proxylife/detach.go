package proxylife

import (
	"os"
	"os/exec"
	"path/filepath"
)

// startDetached starts path with args in its own session or process
// group so it survives the caller's exit. Stdout and stderr are appended
// to logPath (the null device when empty); stdin is the null device. The
// child is released, not awaited.
func startDetached(path string, args []string, logPath string) (pid int, err error) {
	if logPath == "" {
		logPath = os.DevNull
	} else if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
