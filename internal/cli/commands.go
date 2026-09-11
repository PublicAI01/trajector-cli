package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

func (a *app) loginCmd(args []string) int {
	return a.with("usage: trajector login", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Login(a.io())
	})
}

func (a *app) logoutCmd(args []string) int {
	return a.with("usage: trajector logout", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Logout(a.io())
	})
}

func (a *app) enableCmd(args []string) int {
	return a.with("usage: trajector enable", args, 0, func(m *lifecycle.Machine, cwd string) error {
		return m.Enable(cwd, a.io())
	})
}

func (a *app) disableCmd(args []string) int {
	args, purge := takeFlag(args, "--purge")
	return a.with("usage: trajector disable [--purge]", args, 0, func(m *lifecycle.Machine, cwd string) error {
		return m.Disable(cwd, purge, a.io())
	})
}

func (a *app) statusCmd(args []string) int {
	return a.with("usage: trajector status", args, 0, func(m *lifecycle.Machine, cwd string) error {
		return m.Status(cwd, a.io())
	})
}

func (a *app) doctorCmd(args []string) int {
	if len(args) == 0 {
		problems := 0
		exit := a.with("usage: trajector doctor", args, 0, func(m *lifecycle.Machine, cwd string) error {
			var err error
			problems, err = m.Doctor(cwd, a.io())
			return err
		})
		if exit == 0 && problems > 0 {
			return 1
		}
		return exit
	}
	switch args[0] {
	case "requeue":
		return a.requeueCmd(args[1:])
	case "discard":
		return a.discardCmd(args[1:])
	case "bundle":
		return a.bundleCmd(args[1:])
	default:
		doctorUsage(a.stderr)
		return 2
	}
}

// doctorUsage spells out the two exits a quarantined batch has, because
// choosing between them is the whole question a user arrives with.
func doctorUsage(w io.Writer) {
	fmt.Fprint(w, `usage: trajector doctor [bundle | requeue <batch-id>|--all | discard <batch-id>|--all]

  requeue  put a quarantined batch back in the spool to upload again;
           use it once whatever stopped the batch is fixed
  discard  delete a quarantined batch and its rawcalls from this machine
           for good; use it to give up on a batch that will never upload
`)
}

func (a *app) bundleCmd(args []string) int {
	return a.with("usage: trajector doctor bundle", args, 0, func(m *lifecycle.Machine, cwd string) error {
		_, err := m.DoctorBundle(cwd, a.io())
		return err
	})
}

func (a *app) requeueCmd(args []string) int {
	return a.with("usage: trajector doctor requeue <batch-id>|--all", args, 1, func(m *lifecycle.Machine, _ string) error {
		batchID, all := args[0], args[0] == "--all"
		if all {
			batchID = ""
		}
		return m.RequeueRejected(batchID, all, a.io())
	})
}

func (a *app) discardCmd(args []string) int {
	args, confirmed := takeFlag(args, "--yes")
	return a.with("usage: trajector doctor discard <batch-id>|--all [--yes]", args, 1, func(m *lifecycle.Machine, _ string) error {
		batchID, all := args[0], args[0] == "--all"
		if all {
			batchID = ""
		}
		return m.DiscardRejected(batchID, all, confirmed, a.io())
	})
}

func (a *app) upgradeCmd(args []string) int {
	return a.with("usage: trajector upgrade", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Upgrade(a.io())
	})
}

func (a *app) uninstallCmd(args []string) int {
	args, deleteData := takeFlag(args, "--delete-data")
	return a.with("usage: trajector uninstall [--delete-data]", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Uninstall(deleteData, a.io())
	})
}

// hookCmd hosts the commands injected into Claude Code hooks. They must
// never block a session: any failure is reported on stderr with a
// non-blocking exit code, and success is silent — what a session hook
// writes on stdout reaches the model, and what it writes on stderr
// reaches the user.
func (a *app) hookCmd(args []string) int {
	if len(args) == 0 {
		hookUsage(a.stderr)
		return 2
	}
	name, rest := args[0], args[1:]
	switch name {
	case "ensure-proxy":
		// The argument marks an injection that carries no base URL. The
		// proxy is brought up either way: with no route to serve it is
		// still the resident process that uploads what this machine
		// records.
		rest, _ = takeFlag(rest, claudesettings.NoProxyMarker)
		if len(rest) != 0 {
			hookUsage(a.stderr)
			return 2
		}
		hook := a.hookInput()
		m, cwd, err := a.prelude()
		if err != nil {
			return a.fail(err)
		}
		err = m.EnsureProxy(cwd, a.io())
		a.followSession(m, cwd, hook)
		return a.exit(err)
	case "session-end":
		if len(rest) != 0 {
			hookUsage(a.stderr)
			return 2
		}
		// The session is closing: nothing said here is read, and the
		// one thing that lasts past it is a file registered now.
		hook := a.hookInput()
		if m, cwd, err := a.prelude(); err == nil {
			a.followSession(m, cwd, hook)
		}
		return 0
	case "discovery":
		if len(rest) != 0 {
			hookUsage(a.stderr)
			return 2
		}
		// A lost hint is acceptable; a blocked session is not, so every
		// failure here is silent.
		if m, cwd, err := a.prelude(); err == nil {
			m.Discovery(cwd, a.io())
		}
		return 0
	case "read":
		if len(rest) != 1 {
			fmt.Fprintln(a.stderr, "usage: trajector hook read <project-dir>")
			return 2
		}
		m, err := a.machine()
		if err != nil {
			return a.fail(err)
		}
		return a.exit(m.ReadSessionFiles(rest[0], a.io()))
	default:
		fmt.Fprintf(a.stderr, "trajector: unknown hook %q\n", name)
		hookUsage(a.stderr)
		return 2
	}
}

func hookUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: trajector hook <ensure-proxy [--no-proxy]|session-end|discovery>")
}

// hookInput decodes what the session wrote on stdin. A terminal is not
// read: a person running the hook by hand would otherwise wait on it
// for input only a session provides.
func (a *app) hookInput() lifecycle.HookInput {
	if f, ok := a.stdin.(*os.File); ok {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return lifecycle.HookInput{}
		}
	}
	return lifecycle.ReadHookInput(a.stdin)
}

// followSession registers the session file a hook was told about and
// starts the process that reads it. Both are silent on failure: the
// session must not learn what the hook did, and the registry is where
// the outcome is read afterwards.
func (a *app) followSession(m *lifecycle.Machine, cwd string, hook lifecycle.HookInput) {
	if registered, err := m.RegisterSessionFile(cwd, hook); err == nil && registered {
		_ = m.SpawnReader(cwd)
	}
}
