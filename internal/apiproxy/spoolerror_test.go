package apiproxy_test

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func TestSpoolWriteFailureIsCountedUnderItsCause(t *testing.T) {
	onPath := func(op string, errno syscall.Errno) error {
		return &fs.PathError{Op: op, Path: "/spool/20260801/msg_01.json.tmp-1", Err: errno}
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"the quota is reached", fmt.Errorf("spooling: %w", spool.ErrQuotaExceeded), "spool full"},
		{"the disk is full", onPath("write", syscall.ENOSPC), "disk full"},
		{"the user's disk quota is used up", onPath("write", syscall.EDQUOT), "disk full"},
		{"another process removed the temp file", fmt.Errorf("%w: %w", spool.ErrTempRemoved, onPath("chmod", syscall.ENOENT)), "spool temp file removed"},
		{"the day directory is gone", onPath("open", syscall.ENOENT), "spool path missing"},
		{"the directory refuses this user", onPath("mkdir", syscall.EACCES), "spool permission denied"},
		{"the operation is not permitted", onPath("chmod", syscall.EPERM), "spool permission denied"},
		{"the process has too many files open", onPath("open", syscall.EMFILE), "too many open files"},
		{"the system has too many files open", onPath("open", syscall.ENFILE), "too many open files"},
		{"the file system is read-only", onPath("open", syscall.EROFS), "read-only file system"},
		{"the disk reports an input or output error", onPath("sync", syscall.EIO), "disk io error"},
		{"an error with no errno", errors.New("short write"), "spool write failed"},
		{"an errno with no category of its own", onPath("write", syscall.EINVAL), "spool write failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apiproxy.SpoolWriteReason(tc.err); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}
