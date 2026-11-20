package apiproxy

import (
	"errors"
	"io/fs"
	"syscall"

	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// spoolWriteReason names the category a rawcall the spool did not take
// is counted under. The error the operating system gave decides it, and
// the category is all that leaves the proxy's own log. An error that
// fits none stays under abandonSpoolWrite.
//
// Windows reports none of the errnos named here, so there a write error
// is sorted only as far as the portable fs errors go.
func spoolWriteReason(err error) string {
	switch {
	case errors.Is(err, spool.ErrQuotaExceeded):
		return abandonSpoolFull
	case errors.Is(err, spool.ErrTempRemoved):
		return abandonTempRemoved
	case errors.Is(err, fs.ErrNotExist):
		return abandonSpoolMissing
	case errors.Is(err, fs.ErrPermission):
		return abandonSpoolPermission
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return abandonDiskFull
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return abandonOpenFiles
	case errors.Is(err, syscall.EROFS):
		return abandonReadOnly
	case errors.Is(err, syscall.EIO):
		return abandonDiskIO
	}
	return abandonSpoolWrite
}
