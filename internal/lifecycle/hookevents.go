package lifecycle

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

// SessionStarting is the moment a session can begin work: the capture
// proxy is up, the session's file is followed, and the repository is
// observed. The hook that calls this also runs on the later prompts of
// the same session, so every step must be repeatable and cost the
// session nothing the second time; the event named in the input is what
// decides whether this moment is observed at all.
//
// The error returned is the proxy's alone.
func (m *Machine) SessionStarting(cwd string, hook HookInput, io IO) error {
	err := m.EnsureProxy(cwd, io)
	m.followAndObserve(cwd, hook, false)
	return err
}

// SessionProgressed is the moment a session's file has just gained a
// turn's worth of lines: the model stopped, or a batch of tools
// finished. It rides on every turn, so it does one thing: it tells
// the resident process to read that file now. Nothing is reported and
// every failure is silent.
func (m *Machine) SessionProgressed(cwd string, hook HookInput) {
	m.followSession(cwd, hook, false)
}

// SessionEnded is the moment a session closes. Nothing said here would
// be read, so nothing is said and nothing is reported: what outlasts
// the session is the file registered now, read to its end, and what
// the repository looked like at the end.
func (m *Machine) SessionEnded(cwd string, hook HookInput) {
	m.followAndObserve(cwd, hook, true)
}

// ToolUsed is the moment after a session's tool ran. It rides on every
// shell tool use, so it must cost a session as close to nothing as it
// can: what the tool was given and what it answered are what decide
// whether anything is observed, and every failure is silent.
func (m *Machine) ToolUsed(cwd string, hook HookInput) {
	m.ObserveGitSnapshot(cwd, hook)
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
func (m *Machine) followAndObserve(cwd string, hook HookInput, ended bool) {
	m.ObserveGitSnapshot(cwd, hook)
	m.followSession(cwd, hook, ended)
}
