package lifecycle

import (
	"context"
	"path/filepath"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/sessionline"
)

// A session's hooks are the only entry into this device from inside a
// running session, and each hook event is one entry point here: which
// operations an event triggers, the order they run in, and whose
// failure the session is allowed to hear are decided in this package,
// not at the call site that decodes the event.
//
// No hook may block a session. Bringing the proxy up is the one step
// whose failure is reported at all, because the session's own traffic
// goes through it; every other step is silent, and the registry and the
// spool are where what it did is read afterwards.
//
// An event that a session can hear from answers tell: whether this
// session must be told that this device captures nothing of it. The
// sentence and the exit code that carries it belong to the command,
// because no other caller says them; which sessions hear it, and how
// often, is decided here.

// SessionStarting is the moment a session can begin work: the capture
// proxy is up, the session's file is followed, and the repository is
// observed. The hook that calls this also runs on the later prompts of
// the same session, so every step must be repeatable and cost the
// session nothing the second time; the event named in the input is what
// decides whether this moment is observed at all.
//
// It is also the one moment that starts the background check for a
// newer release, once the proxy is settled and whether or not it came
// up: a build whose proxy fails is one a newer release may fix. The
// check runs on its own and says nothing here.
//
// The error returned is the proxy's alone. A session whose proxy could
// not be brought up hears that instead, and stays untold about the
// device: one hook writes one line, and the failure the user can act on
// now is the one worth that line.
func (m *Machine) SessionStarting(ctx context.Context, cwd string, hook HookInput, streams IO) (tell bool, err error) {
	err = m.EnsureProxy(ctx, cwd, streams)
	m.startUpgradeCheck(ctx)
	m.followAndObserve(ctx, cwd, hook, false)
	if err != nil {
		return false, err
	}
	return m.tellSession(cwd, hook), nil
}

// Progressed is what a session hears at the end of a turn: at most one
// of the two lines, and for the second, how the session was started,
// because what the user can do about it depends on that.
type Progressed struct {
	// NothingRecorded: the device captures nothing of the session, as
	// tellSession answers it.
	NothingRecorded bool
	// NoReasoning: most of the reasoning blocks read from the session
	// so far carry no text.
	NoReasoning bool
	// Surface is how the session was started, and is meaningful only
	// with NoReasoning.
	Surface LaunchSurface
}

// LaunchSurface is how Claude Code was started for a session, as far
// as the line said to it is concerned. A value the session's lines
// state that is not one of these, or no value, is SurfaceUnknown.
type LaunchSurface int

const (
	SurfaceUnknown LaunchSurface = iota
	// SurfaceCLI is the interactive command line.
	SurfaceCLI
	// SurfaceDesktop is the desktop app.
	SurfaceDesktop
	// SurfaceSDK is the command line run by a program.
	SurfaceSDK
)

// launchSurface reads the value a session line states as how Claude
// Code was started.
func launchSurface(stated string) LaunchSurface {
	switch stated {
	case "cli":
		return SurfaceCLI
	case "claude-desktop":
		return SurfaceDesktop
	case "sdk-cli":
		return SurfaceSDK
	default:
		return SurfaceUnknown
	}
}

// SessionProgressed is the moment a session's files have just gained a
// turn's worth of lines: the model or a subagent stopped, or a batch of
// tools finished. It rides on every turn, so it does one thing: it
// tells the resident process to read the session's files now. No
// failure of it is reported. A subagent's stop is never told: the
// session hears the notice once, and that once belongs to a turn of
// the session's own, not to the end of a subagent's.
//
// It is also the one moment that tells a session most of its reasoning
// blocks carry no text: while the session runs, so the user can still
// act on it, and from what was read of the session before this hook,
// since the reading the hook asks for runs on its own. A device that
// captures nothing of the session says that instead, once, and says
// nothing of reasoning while it stays so.
func (m *Machine) SessionProgressed(ctx context.Context, cwd string, hook HookInput) Progressed {
	m.followSession(ctx, cwd, hook, false)
	if hook.HookEvent == claudesettings.EventSubagentStop {
		return Progressed{}
	}
	if m.tellSession(cwd, hook) {
		return Progressed{NothingRecorded: true}
	}
	surface, tell := m.tellReasoning(cwd, hook)
	return Progressed{NoReasoning: tell, Surface: surface}
}

// SessionEnded is the moment a session closes. No failure of it is
// reported: what outlasts the session is the file registered now, read
// to its end, and what the repository looked like at the end.
func (m *Machine) SessionEnded(ctx context.Context, cwd string, hook HookInput) (tell bool) {
	m.followAndObserve(ctx, cwd, hook, true)
	return m.tellSession(cwd, hook)
}

// ToolUsed is the moment after a session's tool ran. It rides on every
// shell tool use, so it must cost a session as close to nothing as it
// can: what the tool was given and what it answered are what decide
// whether anything is observed, and every failure is silent.
func (m *Machine) ToolUsed(ctx context.Context, cwd string, hook HookInput) {
	m.ObserveGitSnapshot(ctx, cwd, hook)
}

// followAndObserve is what a session's opening and its close have in
// common. The repository is observed from these two hooks rather than
// from hooks of their own so that opening or closing a session costs
// the user one process, not two.
//
// The repository is observed first. At a session's close, every
// record that close produces must leave in the one flush the close
// asks for, and the follow is what asks for it; a record written
// after it would wait for a later flush. The opening has no such
// flush to catch, so the order costs it nothing.
func (m *Machine) followAndObserve(ctx context.Context, cwd string, hook HookInput, ended bool) {
	m.ObserveGitSnapshot(ctx, cwd, hook)
	m.followSession(ctx, cwd, hook, ended)
}

// tellSession answers whether this session is the one to hear that the
// device captures nothing of it. It answers true once per session — the
// registry records that the session was told — because a line repeated
// on every turn is read as noise and then not read at all. A project
// with no standing grant is never told: what it hears about the device
// would not change its own answer. A routing table that cannot be read
// leaves the grant unknown, and the session is told: its hook runs, so
// it was enabled, and nothing of it is recorded.
//
// Everything about it fails silent. A session must not be blocked by
// the attempt to tell it something, and a device whose registry cannot
// be written is already saying enough.
func (m *Machine) tellSession(cwd string, hook HookInput) bool {
	if hook.SessionPath == "" {
		return false
	}
	st, err := m.observeProject(cwd)
	if err != nil || (!st.Enabled && st.TableUnreadable == nil) {
		return false
	}
	if !report.Recording(m.recordingFacts(st)).StoppedDeviceWide() {
		return false
	}
	first, err := m.registry.Tell(st.Hash, filepath.Clean(hook.SessionPath))
	return err == nil && first
}

// tellReasoning answers whether this session is the one to hear that
// most of its reasoning blocks carry no text, and how it was started.
// The facts are local: the reasoning blocks read so far from the file
// the hook named, of which more are empty than hold text, and the
// launch surface the last line read from that file states. The
// session's agent files are left out: what their blocks hold follows
// the model each agent ran on, which no setting of the user's decides,
// and a line about the user's own session must rest on the session's
// own file. Nothing else is asked, so the line says what this device
// saw and no more.
//
// It answers true at most once a day per project, by the UTC day: the
// registry records the day, and a line repeated on every turn of every
// session is read as noise. A project with no standing grant is never
// told, and neither is a session on a device that captures nothing of
// it: what it would hear is about the records, and there are none; the
// day is not spent on it either. Everything about it fails silent, for
// the reasons tellSession gives.
func (m *Machine) tellReasoning(cwd string, hook HookInput) (LaunchSurface, bool) {
	if hook.SessionPath == "" {
		return SurfaceUnknown, false
	}
	st, err := m.observeProject(cwd)
	if err != nil || !st.Enabled {
		return SurfaceUnknown, false
	}
	if report.Recording(m.recordingFacts(st)).StoppedDeviceWide() {
		return SurfaceUnknown, false
	}
	files, err := m.registry.Files(st.Hash)
	if err != nil {
		return SurfaceUnknown, false
	}
	path := filepath.Clean(hook.SessionPath)
	var read sessionline.ReasoningBlocks
	stated := ""
	for _, f := range files {
		if f.Path == path {
			read, stated = f.Reasoning(), f.LaunchSurface
		}
	}
	if read.Empty == 0 || read.Empty <= read.WithText {
		return SurfaceUnknown, false
	}
	first, err := m.registry.TellReasoning(st.Hash, m.deps.Now())
	if err != nil || !first {
		return SurfaceUnknown, false
	}
	return launchSurface(stated), true
}

// recordingFacts is the part of a diagnosis the recording state is
// decided from, resolved without the cost of the whole: a hook runs on
// every turn of every session and cannot pay for a dashboard to learn
// what the session is entitled to hear. A spool that will not open
// leaves the field it decides zero, which reads as a spool that takes
// writes — a device already unable to open its spool has a louder
// problem than a line in a transcript.
func (m *Machine) recordingFacts(st report.ProjectStatus) report.Diagnosis {
	d := report.Diagnosis{Project: st}
	if sp, err := m.spool(); err == nil {
		d.Spool.WritableErr = sp.Writable()
	}
	return d
}
