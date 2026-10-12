package lifecycle

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
)

// completeEnabledProjects is the repair an installed build makes on
// its own: in each project a standing grant covers, it adds the hooks
// this release installs that the project's injection lacks, and
// reports in how many projects it added any. It only completes what
// enable wrote: a project whose injection is gone
// altogether is a consent question doctor reports and leaves to the
// user, a revoked grant covers nothing, and a project directory that
// is gone has no file to complete. claudesettings.CompleteProjectHooks
// is its one write, so the base URL, the token, the optional settings
// and .gitignore stay as enable or the user left them.
//
// The projects are the ones doctor --all checks, and a root is gone by
// the same test, so the two never disagree about which projects stand.
// A routing table this build cannot read leaves nothing to complete:
// the next command says what it found. A settings file that cannot be
// completed is named in the error after the other projects are done.
func (m *Machine) completeEnabledProjects() (completed int, err error) {
	roots, err := m.grantedRoots()
	if err != nil {
		return 0, err
	}
	hooks := projectHooks(m.deps.ExecPath)
	var errs []error
	for _, root := range roots {
		if rootGone(root) {
			continue
		}
		path := claudesettings.ProjectLocalPath(root)
		added, err := claudesettings.CompleteProjectHooks(path, hooks)
		if err != nil {
			errs = append(errs, fmt.Errorf("completing the session hooks in %s: %w", path, err))
			continue
		}
		if added > 0 {
			completed++
		}
	}
	return completed, errors.Join(errs...)
}

// completedNotice is the line that tells the user how many projects
// an upgrade completed.
func completedNotice(projects int) string {
	return "The session hooks of " + strconv.Itoa(projects) + " enabled project(s) were completed for this release; `trajector doctor --all` lists them."
}
