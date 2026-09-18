package report

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// The sentences more than one surface prints, and the functions that
// decide which of them apply to a project right now. A surface asks for
// the sentences and chooses how to present them; it never pairs the
// facts itself, so status, doctor and enable cannot reach different
// answers about the same project. A sentence about what Claude Code
// will do is hedged, because it is read from configuration on this
// machine and configuration can be overridden from places a static
// reading cannot see.
const (
	// hooksWillLoad and hooksWillNotLoad open the report of a static
	// reading of Claude Code's configuration. The reason a reading
	// found follows hooksWillNotLoad in parentheses.
	hooksWillLoad    = "Judged from configuration readable on this machine, Claude Code will load trajector's hooks in this project"
	hooksWillNotLoad = "Judged from configuration readable on this machine, Claude Code will not load trajector's hooks in this project"
	// proxyHalfOnly is the consequence of hooks that will not load in
	// the shape with a base URL: the proxy records, the session files
	// are not read.
	proxyHalfOnly = "Only the proxy records this project for now; its session files are not read"
	// nothingRecordedNow is the consequence in the shape without a
	// base URL, where the hooks are the only source.
	nothingRecordedNow = "Nothing is recorded from this project for now"
	// noProxyWayOut follows nothingRecordedNow with the one change
	// that records again.
	noProxyWayOut = "Run trajector enable without --no-proxy to record through the proxy instead (/remote-control inside this project becomes unavailable; claude remote-control still works)."

	// remoteControlNotice is said for a project in the shape with a
	// base URL: that shape makes /remote-control unavailable inside
	// the project, and both ways around it are named beside the fact.
	remoteControlNotice = "Remote Control: inside this project, /remote-control will not be available. To use it, either start sessions with claude remote-control (both sources are still recorded), or run trajector enable --no-proxy to record only the session files (Remote Control stays available; with no proxy, none of this project's calls are witnessed)."
	// noProxyShapeFact is said for a project in the shape without a
	// base URL.
	noProxyShapeFact = "This project records from its session files only, so Remote Control stays available."

	// UnwitnessedReward is what a call no proxy of this device handled
	// is worth, said wherever a project is stated to be contributing:
	// the user reads it before anything of theirs is uploaded, not
	// after. It states the rule and no figure — what the rates are is
	// the service's to say and changes without a new build, and a
	// number frozen into a binary would be read as a promise.
	UnwitnessedReward = "Calls the local proxy did not witness are rewarded at a lower rate than witnessed ones. " +
		"A call is witnessed when the proxy handled its request and its response on this machine, so the sessions this project had before you enabled it, " +
		"and any session that runs while the proxy does not, are not witnessed. " +
		"The tokens are counted in full either way; only the amount is reduced. " +
		"The rates themselves are set by the service, not by this build."

	// emptyReasoningFact counts the assistant lines of a project's
	// session files that carry the reasoning field with nothing in it,
	// out of the assistant lines counted with them. It is a fact about
	// the lines and not a fault of them: what fills the field is a
	// setting of Claude Code's.
	emptyReasoningFact = "%d of %d assistant lines carry no reasoning."
	// emptyReasoningWayOut follows that fact only while the setting
	// that fills the field is off for the project. Where the setting is
	// on, what left the field empty is not readable on this machine,
	// and the fact stands with no next step beside it.
	emptyReasoningWayOut = claudesettings.KeyShowThinkingSummaries +
		" is off for this project; run `trajector enable` to turn it on."

	// workspaceNotTrusted is doctor's answer when session files of an
	// enabled project exist that no hook of trajector's reported, and
	// nothing readable on this machine keeps the hooks from loading:
	// Claude Code runs a project's hooks only once the workspace is
	// trusted, and that trust is given in a dialog no file records.
	workspaceNotTrusted = "This workspace is not trusted yet; accept the trust dialog in Claude Code."

	// spellingVariantsNotice is the standing disclosure of what the
	// search for a project's session files cannot find by design.
	spellingVariantsNotice = "Sessions started under another spelling of this project's path, or under a directory name Claude Code was told to use instead, are stored under names this device does not compute and are not collected."

	// RecordingPausedUntilDoctor is what a device still owes its user
	// after a build that cannot read the session files is replaced: a
	// newer binary does not resume recording by itself, and doctor is
	// the command that reads the files and decides whether this build
	// covers them.
	RecordingPausedUntilDoctor = "Recording is paused until you run `trajector doctor`, which checks that this build can read your session files."

	// windowsSideClaudeFact names the one arrangement across a WSL
	// boundary that records nothing, and windowsSideWayOut what makes
	// it record.
	windowsSideClaudeFact = "this project is on a Windows drive mounted into WSL, so Claude Code runs on the Windows side there and its hooks cannot reach this trajector; nothing is recorded from it"
	windowsSideWayOut     = "Run both on the same side: open the project from inside WSL with a Claude Code installed there, or run trajector on the side Claude Code runs on."

	// staleDiscoveryHookFact names a hook trajector wrote into the
	// settings file of the default configuration directory of a device
	// that now names another one: nothing reads it there, so it never
	// runs. staleDiscoveryHookRemoved says the same about a hook this
	// run took out. The well-known spelling of the default directory is
	// the whole of the path either sentence carries, so neither states
	// a path of the user's.
	staleDiscoveryHookFact    = "a trajector hook is left in ~/.claude/settings.json and never runs; Claude Code reads the directory " + claudesettings.ConfigDirEnv + " names instead"
	staleDiscoveryHookRemoved = "removed a trajector hook left in ~/.claude/settings.json; Claude Code reads the directory " + claudesettings.ConfigDirEnv + " names instead"
)

// HookOutlook is what a static reading of Claude Code's configuration
// means for one enabled project: whether the hooks will load and, when
// they will not, what is left recording and what records again. The
// surfaces read the parts they have room for and add their own
// question; none of them holds a sentence of its own about a reading.
type HookOutlook struct {
	// Runs reports that nothing readable on this machine keeps the
	// hooks from loading. Judgement is then the whole outlook.
	Runs bool
	// RecordsNothing reports that the hooks are this project's only
	// source, so nothing is recorded from it while they do not load.
	// It is the one outlook a surface may have to ask about before it
	// acts.
	RecordsNothing bool
	// Judgement states the reading in one sentence, with the reason in
	// parentheses when the hooks will not load. It carries no final
	// period: a surface that continues the sentence adds one.
	Judgement string

	// consequence is what hooks that will not load leave recording,
	// and wayOut the one change that records again. The consequence
	// carries no final period, as Judgement; the way out is a whole
	// sentence, and is empty where another source still records.
	consequence string
	wayOut      string
}

// Lines is the outlook as whole lines, the judgement first: what status
// prints under a project, and what enable prints where it needs no
// answer.
func (o HookOutlook) Lines() []string {
	lines := []string{o.Judgement}
	if o.consequence != "" {
		lines = append(lines, o.consequence)
	}
	if o.wayOut != "" {
		lines = append(lines, o.wayOut)
	}
	return lines
}

// follows is what comes after the judgement as one line of whole
// sentences, which is the shape of a doctor follow-up.
func (o HookOutlook) follows() string {
	if o.wayOut == "" {
		return o.consequence + "."
	}
	return o.consequence + ". " + o.wayOut
}

// ExplainHooks pairs a static reading of the hook configuration with
// the shape the project records in, which is the only place the two
// are read together: what "the hooks will not load" costs the user
// depends on whether anything else records at all.
func ExplainHooks(policy claudesettings.HookPolicy, shape routing.Shape) HookOutlook {
	if policy.Runs {
		return HookOutlook{Runs: true, Judgement: hooksWillLoad}
	}
	outlook := HookOutlook{Judgement: fmt.Sprintf("%s (%s)", hooksWillNotLoad, policy.Reason)}
	if shape == routing.WithoutProxy {
		outlook.RecordsNothing = true
		outlook.consequence, outlook.wayOut = nothingRecordedNow, noProxyWayOut
		return outlook
	}
	outlook.consequence = proxyHalfOnly
	return outlook
}

// ShapeNotice is what the shape a project records in costs or keeps,
// said wherever the shape is stated: status prints it under every
// contributing project, and enable prints it once the install is
// proven.
func ShapeNotice(shape routing.Shape) string {
	if shape == routing.WithoutProxy {
		return noProxyShapeFact
	}
	return remoteControlNotice
}

// EarlierSessionLines is what enable says about the session files a
// project already has: how many are collected once it is enabled and
// how far back they go, and where the search stopped short of the
// whole tree.
func EarlierSessionLines(found discover.Result) []string {
	var lines []string
	if len(found.Sessions) == 0 {
		lines = append(lines, "No earlier session records to collect.")
	} else {
		oldest := found.Oldest.Local()
		lines = append(lines, fmt.Sprintf("%d earlier session record(s) will be collected once; the oldest is from %s.",
			len(found.Sessions), oldest.Format("2006-01-02")))
	}
	if found.Gaps.Truncated {
		lines = append(lines, treeLimitExceeded())
	}
	return lines
}

// treeLimitExceeded says that the count of a project's earlier
// session files stopped short: the directory tree was larger than
// the search visits, and the limit is part of the sentence so the
// number is read against it.
func treeLimitExceeded() string {
	return fmt.Sprintf("This project's directory tree has more than %d directories, so the count above is incomplete.", discover.Limit)
}
