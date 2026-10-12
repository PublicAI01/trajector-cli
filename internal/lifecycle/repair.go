package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"slices"
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
// A routing table this build cannot read leaves nothing to complete:
// the next command says what it found. A settings file that cannot be
// completed is named in the error after the other projects are done.
func (m *Machine) completeEnabledProjects() (completed int, err error) {
	// An unreadable table reads as no grant: nothing is completed.
	grants, _ := m.routes.All()
	var roots []string
	for _, g := range grants {
		if g.Revoked || g.RootPath == "" || slices.Contains(roots, g.RootPath) {
			continue
		}
		roots = append(roots, g.RootPath)
	}
	slices.Sort(roots)
	hooks := projectHooks(m.deps.ExecPath)
	var errs []error
	for _, root := range roots {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
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
