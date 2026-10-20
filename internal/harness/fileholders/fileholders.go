// Package fileholders names the processes that hold a file open, so a
// test whose rename failed because a file was in use can log who used
// it. It asks the system's Restart Manager, which only Windows has;
// elsewhere a rename never fails for that reason and Of names no one.
//
// It is test harness only. What it reports names programs installed on
// the machine it runs on, which must never reach an error message or a
// file a user can be asked to share.
package fileholders

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// Holder is one process that holds a file open.
type Holder struct {
	PID uint32
	// App is the name the Restart Manager gives the process: a
	// program's description, or a service's display name.
	App string
	// Service is the short name of the service the process runs, when
	// it runs one.
	Service string
	// Image is the program file the process runs, when the system
	// tells.
	Image string
}

func (h Holder) String() string {
	s := fmt.Sprintf("pid %d %q", h.PID, h.App)
	if h.Service != "" {
		s += " service " + h.Service
	}
	if h.Image != "" {
		s += " image " + h.Image
	}
	return s
}

// Of is every process that holds path open now. Its error is
// errors.ErrUnsupported where the system cannot tell.
func Of(path string) ([]Holder, error) {
	return of(path)
}

// LogRenameFailure logs, when err is a failed rename, every process
// that holds either of the two files the rename named. It asks after
// the failure, so a holder that let go in between is not named; a
// holder that outlasted every retry of the rename usually still holds.
func LogRenameFailure(t testing.TB, err error) {
	t.Helper()
	link, ok := errors.AsType[*os.LinkError](err)
	if !ok {
		return
	}
	for _, path := range []string{link.Old, link.New} {
		holders, err := Of(path)
		switch {
		case errors.Is(err, errors.ErrUnsupported):
			return
		case err != nil:
			t.Logf("fileholders: cannot tell what holds %s: %v", path, err)
		case len(holders) == 0:
			t.Logf("fileholders: nothing holds %s now", path)
		}
		for _, h := range holders {
			t.Logf("fileholders: %s is held by %s", path, h)
		}
	}
}
