package cli

import (
	"fmt"
	"regexp"
)

const forgetUsage = "usage: trajector forget <session-id>"

// sessionIDShape is what a session id may look like. The id addresses
// records inside the spool and never becomes a path, but an argument
// with whitespace or a slash in it is a mistake, not a session, and is
// refused here rather than matched against nothing in silence.
var sessionIDShape = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// forgetCmd acts on one session by id, not on the working directory, so
// it runs from anywhere: the machine alone is enough.
func (a *app) forgetCmd(args []string) int {
	if code, answered := a.preparse(forgetUsage, args, nil); answered {
		return code
	}
	if len(args) > 1 {
		fmt.Fprintln(a.stderr, forgetUsage)
		return 2
	}
	m, err := a.machine()
	if err != nil {
		return a.fail(err)
	}
	sessionID := m.CurrentSessionID()
	if len(args) == 1 {
		sessionID = args[0]
	}
	if sessionID == "" {
		fmt.Fprintln(a.stderr, forgetUsage)
		fmt.Fprintln(a.stderr, "trajector: no session id given: run it inside a Claude Code session, or pass the session id")
		return 2
	}
	if !sessionIDShape.MatchString(sessionID) {
		fmt.Fprintln(a.stderr, forgetUsage)
		fmt.Fprintf(a.stderr, "trajector: %q is not a session id: only letters, digits, '.', '_' and '-' can appear in one\n", sessionID)
		return 2
	}
	return a.exit(m.Forget(sessionID, a.io()))
}
