package lifecycle

import "github.com/PublicAI01/trajector-cli/internal/report"

// allProjectsWithoutProxy reports that every enabled project on this
// device forgoes the proxy. On such a device the resident process lives
// only while a session runs, which is the healthy state, not a fault: a
// surface must not read a proxy that is absent between sessions as
// something to repair. It is false when no project is enabled, because
// then there is nothing the reading is about.
func allProjectsWithoutProxy(projects []report.ProjectStatus) bool {
	enabled := 0
	for _, p := range projects {
		if !p.Enabled {
			continue
		}
		enabled++
		if !p.NoProxy {
			return false
		}
	}
	return enabled > 0
}
