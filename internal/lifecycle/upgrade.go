package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// managerHandback is how to move an installation its package manager
// owns: the manager's name as the user knows it, and the command that
// does the upgrade. One entry per manager selfupdate can recognize.
var managerHandback = map[selfupdate.Manager]struct{ name, command string }{
	selfupdate.Homebrew: {"Homebrew", "brew upgrade trajector"},
	selfupdate.Scoop:    {"Scoop", "scoop update trajector"},
}

// upgradeLockWait is how long a manual upgrade waits for one already
// running on this machine — the background checker, or another
// terminal — before saying so. The one running takes minutes when it
// downloads, so waiting it out would look like a hang.
const upgradeLockWait = 2 * time.Second

// installation is this machine's trajector as selfupdate sees it.
func (m *Machine) installation() selfupdate.Installation {
	return selfupdate.Installation{
		ExecPath: m.deps.ExecPath,
		Version:  m.deps.Version,
		State:    m.deps.Layout.UpgradeState(),
		Lock:     m.deps.Layout.UpgradeLock(),
	}
}

// Upgrade replaces this binary with the newest published release and
// says which of the four things happened to it.
//
// Which release that is comes from the release source alone: the
// service's minimum client version decides whether a machine must
// upgrade, never what it upgrades to. Every exit that does not replace
// the binary leaves it exactly as it was — an upgrade that cannot
// happen must never cost the user a working trajector.
func (m *Machine) Upgrade(ctx context.Context, streams IO) error {
	out, err := selfupdate.Upgrade(ctx, m.installation(), m.deps.Releases, upgradeLockWait)
	if errors.Is(err, selfupdate.ErrBusy) {
		return errors.New("another upgrade is running on this machine; try again in a minute")
	}
	if unusable, ok := errors.AsType[*selfupdate.UnusableError](err); ok {
		fmt.Fprintf(streams.Out, "trajector %s does not start on this machine, so %s was kept.\n", unusable.Version, m.deps.Version)
		return fmt.Errorf("version %s was not installed: %s", unusable.Version, unusable.Reason)
	}
	if err != nil {
		return err
	}
	switch out.Kind {
	case selfupdate.Managed:
		hand := managerHandback[out.Manager]
		fmt.Fprintf(streams.Out, "This trajector was installed with %s, which owns the binary.\n", hand.name)
		fmt.Fprintf(streams.Out, "Run `%s` instead.\n", hand.command)
	case selfupdate.NotARelease:
		fmt.Fprintf(streams.Out, "This build reports version %s, which is not a published release.\n", out.From)
		fmt.Fprintln(streams.Out, "Nothing was changed.")
	case selfupdate.AlreadyNewest:
		fmt.Fprintf(streams.Out, "trajector %s is already the newest release.\n", out.From)
	case selfupdate.Upgraded:
		fmt.Fprintf(streams.Out, "Upgraded trajector %s -> %s.\n", out.From, out.To)
		// The proxy this build started keeps serving until something
		// replaces it; the takeover rule then prefers the newer release.
		fmt.Fprintln(streams.Out, "A proxy from the previous build may still be running; the next session replaces it.")
		said := m.sayPauseStands(streams)
		// The build just installed has left its lines for the next
		// command; this one is that command, and the user is here. What
		// it already said above is not said twice: that it upgraded,
		// which the new build words as having happened in the
		// background, and the step a pause still asks for.
		m.tellNotices(streams, upgradedNotice(out.To), said)
	default:
		// Every outcome owes the user a sentence. An outcome no case
		// above answers is a gap in this switch, and saying nothing at
		// all would hide it behind a successful command.
		return fmt.Errorf("upgrade finished in a state this build cannot describe (%d)", out.Kind)
	}
	return nil
}

// sayPauseStands names what a pause that survived this upgrade still
// asks of the user. Which pauses those are, and what each of them then
// owes, is the pause reason's own answer: a newer build resumes nothing
// by itself. The pause is read here, never lifted. A routing table this
// build cannot read leaves the line out: the upgrade is already done,
// and a failure to add a hint to it must not report the upgrade as
// failed. It returns the line it said, empty when it said none.
func (m *Machine) sayPauseStands(streams IO) string {
	paused, err := m.routes.PausedReason()
	if err != nil {
		return ""
	}
	next := paused.ExplainAfterUpgrade()
	if next != "" {
		fmt.Fprintln(streams.Out, next)
	}
	return next
}

// TakeNotices returns the lines this device has waiting for its user,
// oldest first, and removes them: each is shown by one command. A store
// that cannot be read gives none. The lines are hints after the fact,
// and a command the user ran for another reason must not fail over one.
func (m *Machine) TakeNotices() []string {
	lines, err := m.notices.Take()
	if err != nil {
		return nil
	}
	return lines
}

// tellNotices takes the waiting lines and says each on stderr, except
// the ones in said, which this command has already put in its own
// words.
func (m *Machine) tellNotices(streams IO, said ...string) {
	for _, line := range m.TakeNotices() {
		if !slices.Contains(said, line) {
			fmt.Fprintln(streams.Err, line)
		}
	}
}

// upgradedNotice is the line an installed build leaves for the next
// command, naming itself.
func upgradedNotice(version string) string {
	return "trajector upgraded itself to " + version + " in the background."
}

// verifyAddr is where a newly installed build starts the proxy it
// checks itself with: loopback, on a port the system picks. The port
// every session reaches belongs to the proxy already serving there,
// which an upgrade does not touch.
const verifyAddr = "127.0.0.1:0"

// VerifyUpgrade is this build showing, right after an older build put
// it in place, that it can serve on this machine. It starts its proxy
// in this process, in a directory of its own with every trajector
// directory inside it and on a port of its own, asks it for its health
// the way a session's hook does, stops it, and removes the directory.
// An error is why it could not; the older build then puts itself back.
//
// Beyond making that directory in the state directory, nothing here
// reads or writes this device's own state, and nothing can leave the
// machine: the device token is not where this proxy looks for it, so
// it uploads nothing, and the OS keyring is never opened, so the check
// cannot stop to ask the user anything.
func (m *Machine) VerifyUpgrade(ctx context.Context) error {
	dir, err := m.deps.Layout.NewUpgradeVerifyDir()
	if err != nil {
		return fmt.Errorf("creating a directory to start the proxy in: %w", err)
	}
	defer os.RemoveAll(dir)
	env := map[string]string{}
	userdirs.Isolate(func(key, value string) { env[key] = value }, dir)
	layout, err := userdirs.Resolve(userdirs.Env{GOOS: runtime.GOOS, Getenv: func(key string) string { return env[key] }})
	if err != nil {
		return err
	}

	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", verifyAddr)
	if err != nil {
		return fmt.Errorf("binding %s: %w", verifyAddr, err)
	}
	// The same assembly a session's proxy is built from, on the
	// directories inside dir, except for where the device token is
	// looked for: a store that may consult the OS keyring would find this
	// device's real token there.
	a := Open(Deps{
		Layout:      layout,
		PlatformURL: m.deps.PlatformURL,
		Version:     m.deps.Version,
		ExecPath:    m.deps.ExecPath,
		ProxyAddr:   l.Addr().String(),
		Home:        dir,
	}).assembly()
	a.Tokens = tokenstore.Files(layout.SecretsDir())
	a.Listener = l
	serveCtx, stop := context.WithCancel(ctx)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- proxyserve.Serve(serveCtx, a, 0, io.Discard, io.Discard) }()

	v := proxylife.For(layout, m.deps.Version, m.deps.ExecPath, a.Addr, nil).Settled(ctx)
	stop()
	serveErr := <-served
	if errors.Is(serveErr, context.Canceled) {
		serveErr = nil
	}
	switch {
	case v.Holder != proxylife.HolderOurs && serveErr != nil:
		return fmt.Errorf("the proxy did not start: %w", serveErr)
	case v.Holder == proxylife.HolderNone:
		return fmt.Errorf("nothing answered at %s", a.Addr)
	case v.Holder != proxylife.HolderOurs:
		return fmt.Errorf("the proxy at %s did not prove itself trajector's: %w", a.Addr, v.Reason)
	case v.Health.Version != m.deps.Version:
		return fmt.Errorf("the proxy reports version %s, not %s", v.Health.Version, m.deps.Version)
	case serveErr != nil:
		return fmt.Errorf("the proxy did not stop cleanly: %w", serveErr)
	}
	return nil
}

// FinishUpgrade is what this build does once an older build has
// committed the install of it: it leaves the user's next command the
// lines that say what happened and what it asks of them. Those are
// that this build is installed; that the data agreement changed with
// it, when this device accepted an earlier one; and what a pause still
// asks once the build is replaced, when one stands.
//
// The agreement pause itself is not set here: the next session sets
// it, as it does for any build that finds the agreement changed. A
// consent record or routing table this build cannot read leaves its
// line out, and that session says what it found instead. Running it
// again adds nothing that still waits.
func (m *Machine) FinishUpgrade(context.Context) error {
	lines := []string{upgradedNotice(m.deps.Version)}
	if accepted, _, err := m.consent.AcceptedVersion(); err == nil && accepted != "" && accepted != consent.AgreementVersion {
		lines = append(lines, "trajector: "+routing.PauseConsentReconfirm.Explain())
	}
	if paused, err := m.routes.PausedReason(); err == nil {
		if next := paused.ExplainAfterUpgrade(); next != "" {
			lines = append(lines, next)
		}
	}
	return m.notices.Add(lines...)
}
