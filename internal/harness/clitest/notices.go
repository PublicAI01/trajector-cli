package clitest

import "github.com/PublicAI01/trajector-cli/internal/notice"

// LeaveNotice puts lines where a build leaves them for the next command
// a person runs, as an upgrade in the background does.
func (e *Env) LeaveNotice(lines ...string) {
	e.t.Helper()
	if err := notice.Open(e.Layout().Notices()).Add(lines...); err != nil {
		e.t.Fatal(err)
	}
}

// WaitingNotices is every line still left for the next command. It
// takes them, so a test reads it once, last.
func (e *Env) WaitingNotices() []string {
	e.t.Helper()
	lines, err := notice.Open(e.Layout().Notices()).Take()
	if err != nil {
		e.t.Fatal(err)
	}
	return lines
}
