package cli

import (
	"fmt"
	"os"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/routing"
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
	// The flag is spelled exactly as the marker the injected hook
	// carries, so the one word names the shape everywhere it shows.
	args, noProxy := takeFlag(args, claudesettings.NoProxyMarker)
	shape := routing.WithProxy
	if noProxy {
		shape = routing.WithoutProxy
	}
	return a.with("usage: trajector enable [--no-proxy]", args, 0, func(m *lifecycle.Machine, cwd string) error {
		return m.Enable(cwd, shape, a.io())
	})
}

func (a *app) disableCmd(args []string) int {
	args, purge := takeFlag(args, "--purge")
	return a.with("usage: trajector disable [--purge]", args, 0, func(m *lifecycle.Machine, cwd string) error {
		return m.Disable(cwd, purge, a.io())
	})
}

// statusCmd prints the dashboard. A dashboard that named something
// broken exits non-zero: a user who runs status from a script must be
// able to tell a device that records from one that stopped, without
// reading the text.
func (a *app) statusCmd(args []string) int {
	problems := 0
	exit := a.with("usage: trajector status", args, 0, func(m *lifecycle.Machine, cwd string) error {
		var err error
		problems, err = m.Status(cwd, a.io())
		return err
	})
	if exit == 0 && problems > 0 {
		return 1
	}
	return exit
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
	// Only the subcommand slot is judged here: what follows belongs to
	// the subcommand, and its own pre-parse knows which flags it takes.
	if code, answered := a.preparse(doctorUsage, args[:1], nil); answered {
		return code
	}
	switch args[0] {
	case "requeue":
		return a.requeueCmd(args[1:])
	case "discard":
		return a.discardCmd(args[1:])
	case "bundle":
		return a.bundleCmd(args[1:])
	default:
		fmt.Fprintln(a.stderr, doctorUsage)
		return 2
	}
}

// doctorUsage spells out the two exits a quarantined batch has, because
// choosing between them is the whole question a user arrives with.
const doctorUsage = `usage: trajector doctor [bundle | requeue <batch-id>|--all | discard <batch-id>|--all]

  requeue  put a quarantined batch back in the spool to upload again;
           use it once whatever stopped the batch is fixed
  discard  delete a quarantined batch and its rawcalls from this machine
           for good; use it to give up on a batch that will never upload`

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
	}, "--all")
}

func (a *app) discardCmd(args []string) int {
	args, confirmed := takeFlag(args, "--yes")
	return a.with("usage: trajector doctor discard <batch-id>|--all [--yes]", args, 1, func(m *lifecycle.Machine, _ string) error {
		batchID, all := args[0], args[0] == "--all"
		if all {
			batchID = ""
		}
		return m.DiscardRejected(batchID, all, confirmed, a.io())
	}, "--all")
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
	// A hook runs where nobody is watching, so a mistyped flag must be
	// refused before the hook name is read as an event and before any
	// session file is looked for.
	if code, answered := a.preparse(hookUsage, args, []string{claudesettings.NoProxyMarker}); answered {
		return code
	}
	if len(args) == 0 {
		fmt.Fprintln(a.stderr, hookUsage)
		return 2
	}
	name, rest := args[0], args[1:]
	switch name {
	case claudesettings.HookEnsureProxy:
		// The argument marks an injection that carries no base URL. The
		// proxy is brought up either way: with no route to serve it is
		// still the resident process that uploads what this machine
		// records.
		rest, _ = takeFlag(rest, claudesettings.NoProxyMarker)
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		m, cwd, err := a.prelude()
		if err != nil {
			return a.fail(err)
		}
		tell, err := m.SessionStarting(cwd, hook, a.io())
		if exit := a.exit(err); exit != 0 {
			return exit
		}
		return a.tellSession(tell)
	case claudesettings.HookSessionEnd:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		if m, cwd, err := a.prelude(); err == nil {
			return a.tellSession(m.SessionEnded(cwd, hook))
		}
		return 0
	case claudesettings.HookGitSnapshot:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		if m, cwd, err := a.prelude(); err == nil {
			m.ToolUsed(cwd, hook)
		}
		return 0
	case claudesettings.HookProgress:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		if m, cwd, err := a.prelude(); err == nil {
			return a.tellSession(m.SessionProgressed(cwd, hook))
		}
		return 0
	case claudesettings.HookDiscovery:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		// A lost hint is acceptable; a blocked session is not, so every
		// failure here is silent.
		if m, cwd, err := a.prelude(); err == nil {
			m.Discovery(cwd, a.io())
		}
		return 0
	case claudesettings.HookRead:
		if len(rest) != 1 {
			fmt.Fprintf(a.stderr, "usage: trajector hook %s <project-dir>\n", claudesettings.HookRead)
			return 2
		}
		m, err := a.machine()
		if err != nil {
			return a.fail(err)
		}
		m.ReadSessionFiles(rest[0], a.io())
		return 0
	default:
		fmt.Fprintf(a.stderr, "trajector: unknown hook %q\n", name)
		fmt.Fprintln(a.stderr, hookUsage)
		return 2
	}
}

// nothingRecordedNotice is the one line a running session is told
// when this device captures nothing of it. It names the command that
// says why, and nothing else: a hook has one line of the user's
// attention and must spend it on where to look, not on which of
// several reasons holds. It goes on the hook's stderr with a non-zero
// exit so that Claude Code shows it — a hook that exits 0 keeps its
// stderr in the transcript, and a hook that exits 2 blocks the
// session, which no hook of trajector's may ever do. Everything else
// about the hook has already succeeded by the time this is said.
const nothingRecordedNotice = "trajector: nothing of this session is being recorded; run trajector status"

func (a *app) tellSession(stopped bool) int {
	if !stopped {
		return 0
	}
	fmt.Fprintln(a.stderr, nothingRecordedNotice)
	return 1
}

// hookUsage lists every hook entry point in the spelling a settings
// file carries, so the usage text cannot drift from what the command
// line dispatches on.
const hookUsage = "usage: trajector hook <" +
	claudesettings.HookEnsureProxy + " [" + claudesettings.NoProxyMarker + "]|" +
	claudesettings.HookSessionEnd + "|" +
	claudesettings.HookGitSnapshot + "|" +
	claudesettings.HookProgress + "|" +
	claudesettings.HookDiscovery + "|" +
	claudesettings.HookRead + ">"

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
