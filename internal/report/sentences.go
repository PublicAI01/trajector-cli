package report

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
)

// The sentences more than one surface prints, spelled once. enable
// says each at the moment it applies; status repeats the standing
// ones every time; doctor reuses the ones it needs. A sentence about
// what Claude Code will do is hedged, because it is read from
// configuration on this machine and configuration can be overridden
// from places a static reading cannot see.
const (
	// hooksWillLoad and HooksWillNotLoad open the report of a static
	// reading of Claude Code's configuration. The reason a reading
	// found follows HooksWillNotLoad in parentheses.
	hooksWillLoad    = "Judged from configuration readable on this machine, Claude Code will load trajector's hooks in this project"
	HooksWillNotLoad = "Judged from configuration readable on this machine, Claude Code will not load trajector's hooks in this project"
	// ProxyHalfOnly is the consequence of hooks that will not load in
	// the shape with a base URL: the proxy records, the session files
	// are not read.
	ProxyHalfOnly = "Only the proxy records this project for now; its session files are not read"
	// nothingRecordedNow is the consequence in the shape without a
	// base URL, where the hooks are the only source.
	nothingRecordedNow = "Nothing is recorded from this project for now"
	// noProxyWayOut follows nothingRecordedNow with the one change
	// that records again.
	noProxyWayOut = "Run trajector enable without --no-proxy to record through the proxy instead (/remote-control inside this project becomes unavailable; claude remote-control still works)."

	// RemoteControlNotice is said for a project in the shape with a
	// base URL: that shape makes /remote-control unavailable inside
	// the project, and both ways around it are named beside the fact.
	RemoteControlNotice = "Remote Control: inside this project, /remote-control will not be available. To use it, either start sessions with claude remote-control (both sources are still recorded), or run trajector enable --no-proxy to record only the session files (Remote Control stays available; records from one source may be rewarded differently)."
	// NoProxyShapeFact is said for a project in the shape without a
	// base URL.
	NoProxyShapeFact = "This project records from its session files only, so Remote Control stays available."

	// workspaceNotTrusted is doctor's answer when session files of an
	// enabled project exist that no hook of trajector's reported, and
	// nothing readable on this machine keeps the hooks from loading:
	// Claude Code runs a project's hooks only once the workspace is
	// trusted, and that trust is given in a dialog no file records.
	workspaceNotTrusted = "This workspace is not trusted yet; accept the trust dialog in Claude Code."

	// spellingVariantsNotice is the standing disclosure of what the
	// search for a project's session files cannot find by design.
	spellingVariantsNotice = "Sessions started under another spelling of this project's path, or under a directory name Claude Code was told to use instead, are stored under names this device does not compute and are not collected."

	// windowsSideClaudeFact names the one arrangement across a WSL
	// boundary that records nothing, and windowsSideWayOut what makes
	// it record.
	windowsSideClaudeFact = "this project is on a Windows drive mounted into WSL, so Claude Code runs on the Windows side there and its hooks cannot reach this trajector; nothing is recorded from it"
	windowsSideWayOut     = "Run both on the same side: open the project from inside WSL with a Claude Code installed there, or run trajector on the side Claude Code runs on."
)

// TreeLimitExceeded says that the count of a project's earlier
// session files stopped short: the directory tree was larger than
// the search visits, and the limit is part of the sentence so the
// number is read against it.
func TreeLimitExceeded() string {
	return fmt.Sprintf("This project's directory tree has more than %d directories, so the count above is incomplete.", discover.Limit)
}
