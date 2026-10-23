package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func (a *app) loginCmd(ctx context.Context, args []string) int {
	return a.with("usage: trajector login", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Login(ctx, a.io())
	})
}

func (a *app) logoutCmd(ctx context.Context, args []string) int {
	return a.with("usage: trajector logout", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Logout(ctx, a.io())
	})
}

// noEarlierFlag asks enable to leave the project's earlier session
// files alone. It stands beside --no-proxy and neither implies the
// other: the shape decides where this project's traffic goes, and this
// decides whether the sessions it already has are collected.
const noEarlierFlag = "--no-earlier"

func (a *app) enableCmd(ctx context.Context, args []string) int {
	// The shape flag is spelled exactly as the marker the injected hook
	// carries, so the one word names the shape everywhere it shows.
	args, taken := takeFlags(args, claudesettings.NoProxyMarker, noEarlierFlag)
	choices := lifecycle.EnableChoices{Shape: routing.WithProxy, SkipEarlier: taken[noEarlierFlag]}
	if taken[claudesettings.NoProxyMarker] {
		choices.Shape = routing.WithoutProxy
	}
	return a.with("usage: trajector enable [--no-proxy] [--no-earlier]", args, 0, func(m *lifecycle.Machine, cwd string) error {
		return m.Enable(ctx, cwd, choices, a.io())
	}, claudesettings.NoProxyMarker, noEarlierFlag)
}

func (a *app) disableCmd(ctx context.Context, args []string) int {
	args, purge := takeFlag(args, "--purge")
	return a.with("usage: trajector disable [--purge]", args, 0, func(m *lifecycle.Machine, cwd string) error {
		return m.Disable(ctx, cwd, purge, a.io())
	})
}

// statusCmd prints the dashboard. A dashboard that named something
// broken exits non-zero: a user who runs status from a script must be
// able to tell a device that records from one that stopped, without
// reading the text.
func (a *app) statusCmd(ctx context.Context, args []string) int {
	problems := 0
	exit := a.withMachine(a.surfaceMachine, "usage: trajector status", args, 0, func(m *lifecycle.Machine, cwd string) error {
		var err error
		problems, err = m.Status(ctx, cwd, a.io())
		return err
	})
	if exit == 0 && problems > 0 {
		return 1
	}
	return exit
}

// doctorAllFlag asks doctor for every project this device records, not
// only the current one. It is read only in the first slot: in a
// subcommand's slot the same word names every quarantined batch.
const doctorAllFlag = "--all"

func (a *app) doctorCmd(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == doctorAllFlag {
		run, usage := (*lifecycle.Machine).Doctor, "usage: trajector doctor"
		if len(args) > 0 {
			run, usage, args = (*lifecycle.Machine).DoctorAll, doctorUsage, args[1:]
		}
		problems := 0
		exit := a.with(usage, args, 0, func(m *lifecycle.Machine, cwd string) error {
			var err error
			problems, err = run(m, ctx, cwd, a.io())
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
		return a.bundleCmd(ctx, args[1:])
	default:
		fmt.Fprintln(a.stderr, doctorUsage)
		return 2
	}
}

// doctorUsage spells out the two exits a quarantined batch has, because
// choosing between them is the whole question a user arrives with.
const doctorUsage = `usage: trajector doctor [--all | bundle | requeue <batch-id>|--all | discard <batch-id>|--all]

  --all    check every project enabled on this device, not only this one,
           each under its own heading
  requeue  put a quarantined batch back in the spool to upload again;
           use it once whatever stopped the batch is fixed
  discard  delete a quarantined batch and its rawcalls from this machine
           for good; use it to give up on a batch that will never upload`

func (a *app) bundleCmd(ctx context.Context, args []string) int {
	return a.withMachine(a.surfaceMachine, "usage: trajector doctor bundle", args, 0, func(m *lifecycle.Machine, cwd string) error {
		_, err := m.DoctorBundle(ctx, cwd, a.io())
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

func (a *app) upgradeCmd(ctx context.Context, args []string) int {
	return a.with("usage: trajector upgrade", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Upgrade(ctx, a.io())
	})
}

func (a *app) uninstallCmd(ctx context.Context, args []string) int {
	args, deleteData := takeFlag(args, "--delete-data")
	return a.with("usage: trajector uninstall [--delete-data]", args, 0, func(m *lifecycle.Machine, _ string) error {
		return m.Uninstall(ctx, deleteData, a.io())
	})
}

// hookCmd hosts the commands injected into Claude Code hooks. They must
// never block a session: any failure is reported on stderr with a
// non-blocking exit code, which Claude Code shows the user as a hook
// error, and success is silent. Plain text on stdout reaches the model
// on some events, so a hook writes nothing there except the one JSON
// message that tells the user something (tellUser).
func (a *app) hookCmd(ctx context.Context, args []string) int {
	// A hook runs where nobody is watching, so a mistyped flag must be
	// refused before the hook name is read as an event and before any
	// session file is looked for.
	if code, answered := a.preparse(hookUsage, args, []string{claudesettings.NoProxyMarker, lifecycle.UpgradeCheckDetached}); answered {
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
		rest, noProxy := takeFlag(rest, claudesettings.NoProxyMarker)
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		m, cwd, err := a.prelude()
		if err != nil {
			return a.fail(err)
		}
		// Starting a session may say other things on stderr first — a
		// stale agreement, a moved upstream — and they are held back:
		// a build that cannot start its proxy must have its sentence
		// on the first line, the one Claude Code shows. A session told
		// that nothing of it is recorded hears them in the same
		// message, before that notice.
		var said bytes.Buffer
		streams := a.io()
		streams.Err = &said
		tell, err := m.SessionStarting(ctx, cwd, hook, streams)
		if cannot, ok := errors.AsType[*proxylife.CannotStartError](err); ok {
			exit := a.cannotStart(cannot, !noProxy)
			fmt.Fprint(a.stderr, said.String())
			return exit
		}
		if err == nil && tell {
			return a.tellUser(said.String() + nothingRecordedNotice)
		}
		fmt.Fprint(a.stderr, said.String())
		return a.exit(err)
	case claudesettings.HookSessionEnd:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		// Claude Code drops a SessionEnd hook's JSON message. The one
		// output of this event it shows is a failed hook's stderr,
		// printed to the terminal after the session closes, so the
		// notice takes that way here and only here.
		if m, cwd, err := a.prelude(); err == nil && m.SessionEnded(ctx, cwd, hook) {
			fmt.Fprintln(a.stderr, nothingRecordedNotice)
			return 1
		}
		return 0
	case claudesettings.HookGitSnapshot:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		if m, cwd, err := a.prelude(); err == nil {
			m.ToolUsed(ctx, cwd, hook)
		}
		return 0
	case claudesettings.HookProgress:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		hook := a.hookInput()
		if m, cwd, err := a.prelude(); err == nil {
			return a.tellProgressed(m.SessionProgressed(ctx, cwd, hook))
		}
		return 0
	case claudesettings.HookDiscovery:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		// A lost hint is acceptable; a blocked session is not, so every
		// failure here is silent. The hint goes out as a message for the
		// user: this hook runs at SessionStart, where plain text on
		// stdout is sent to the model and not shown.
		m, cwd, err := a.prelude()
		if err != nil {
			return 0
		}
		var hint bytes.Buffer
		streams := a.io()
		streams.Out = &hint
		m.Discovery(cwd, streams)
		if hint.Len() == 0 {
			return 0
		}
		return a.tellUser(hint.String())
	case claudesettings.HookRead:
		if len(rest) != 1 {
			fmt.Fprintf(a.stderr, "usage: trajector hook %s <project-dir>\n", claudesettings.HookRead)
			return 2
		}
		m, err := a.machine()
		if err != nil {
			return a.fail(err)
		}
		m.ReadSessionFiles(ctx, rest[0], a.io())
		return 0
	case claudesettings.HookUpgradeCheck:
		// The background check for a newer release, which the session
		// hook starts. The process the hook starts only starts the one
		// that checks, marked as such, and returns at once: the hook's
		// own child is stopped with the hook, and the check is not.
		// Neither says anything: what the check found is in the upgrade
		// state file, and what the user must hear waits for their next
		// command.
		rest, detached := takeFlag(rest, lifecycle.UpgradeCheckDetached)
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		m, err := a.machine()
		if err != nil {
			return a.fail(err)
		}
		if !detached {
			if err := m.DetachUpgradeCheck(ctx); err != nil {
				return a.fail(err)
			}
			return 0
		}
		m.CheckForUpgrade(ctx)
		return 0
	case claudesettings.HookUpgradeVerify:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		// The check starts a proxy on directories of its own and reads
		// nothing the user config names, so a config file this build
		// cannot read is no reason to refuse the build being installed.
		m, err := a.surfaceMachine()
		if err != nil {
			return a.fail(err)
		}
		if err := m.VerifyUpgrade(ctx); err != nil {
			return a.fail(err)
		}
		return 0
	case claudesettings.HookUpgradeFinish:
		if len(rest) != 0 {
			fmt.Fprintln(a.stderr, hookUsage)
			return 2
		}
		m, err := a.machine()
		if err != nil {
			return a.fail(err)
		}
		if err := m.FinishUpgrade(ctx); err != nil {
			return a.fail(err)
		}
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
// several reasons holds.
const nothingRecordedNotice = "trajector: nothing of this session is being recorded; run trajector status"

// hookMessage is the JSON a hook writes on stdout to have Claude Code
// show the user a message. Claude Code shows systemMessage in the
// session as "<event> says: <message>", keeps it out of what the model
// is sent, and lets the session go on.
type hookMessage struct {
	SystemMessage string `json:"systemMessage"`
}

// tellUser has Claude Code show message to the user, and exits 0. A
// notice is not a failure: stderr with a non-zero exit is shown as a
// hook error, and exit 2 blocks the session, which no hook of
// trajector's may ever do. Everything else about the hook has already
// succeeded by the time this is said.
func (a *app) tellUser(message string) int {
	out, err := json.Marshal(hookMessage{SystemMessage: strings.TrimSuffix(message, "\n")})
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "%s\n", out)
	return 0
}

// noReasoningLines are the lines a running session is told when most
// of its reasoning blocks carry no text, one for each way Claude Code
// can be started: each names the one step that turns the summaries on
// there. They state what this device read and nothing more.
var noReasoningLines = map[lifecycle.LaunchSurface]string{
	lifecycle.SurfaceCLI:     "trajector: no reasoning summaries in this session; run trajector enable, then start a new session",
	lifecycle.SurfaceDesktop: "trajector: no reasoning summaries; set Default transcript view to Thinking, then start a new session",
	lifecycle.SurfaceSDK:     "trajector: no reasoning summaries in this session; add --thinking-display summarized and rerun",
	lifecycle.SurfaceUnknown: "trajector: no reasoning summaries in this session; run trajector status to see how to turn them on",
}

// tellProgressed says what a session hears at the end of a turn. The
// device capturing nothing comes first: it is the line the user can
// least afford to miss.
func (a *app) tellProgressed(p lifecycle.Progressed) int {
	switch {
	case p.NothingRecorded:
		return a.tellUser(nothingRecordedNotice)
	case p.NoReasoning:
		return a.tellUser(noReasoningLines[p.Surface])
	default:
		return 0
	}
}

// cannotStart tells a session that this build cannot start its proxy.
// The sentence is the first line on stderr because that line is what
// Claude Code shows of a hook that exits non-zero; the exit is 1, never
// 2, so the session goes on. A session whose requests are routed
// through the proxy is told next that they fail too. What the last
// start observed follows on a line of its own, for whoever reads the
// whole of it.
func (a *app) cannotStart(cannot *proxylife.CannotStartError, routed bool) int {
	fmt.Fprintln(a.stderr, report.CannotStart(cannot.Version))
	if routed {
		fmt.Fprintln(a.stderr, report.CannotStartRouted)
	}
	fmt.Fprintf(a.stderr, "trajector: %v\n", cannot.Cause)
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
	claudesettings.HookRead + "|" +
	claudesettings.HookUpgradeCheck + "|" +
	claudesettings.HookUpgradeVerify + "|" +
	claudesettings.HookUpgradeFinish + ">"

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
