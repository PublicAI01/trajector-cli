package report

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// AutoUpgradeState is how this device keeps trajector up to date on its
// own: whether it does, and what the last background check found.
type AutoUpgradeState struct {
	// NotRelease says this binary was not built by the release
	// pipeline: a build made from source never checks on its own,
	// whatever the user config says.
	NotRelease bool
	// Off says the user config file turns automatic upgrades off.
	Off bool
	// NeedsAgreement says the data agreement this device accepted, if
	// it accepted one, does not tell the user about the check. The
	// check starts once the user accepts the current one.
	NeedsAgreement bool
	// ConfigErr, when non-nil, is why the user config file could not be
	// read. Automatic upgrades are then off, whatever the file says.
	ConfigErr error
	// StateErr, when non-nil, is why the upgrade state file could not
	// be read; the fields below are then zero.
	StateErr error
	// CheckedAt is when the background check last ran, zero when it
	// never has, and Result what it came to.
	CheckedAt time.Time
	Result    selfupdate.Result
	// Installed is the release the last upgrade installed.
	Installed string
	// Manager is the package manager that owns this installation, by
	// the name its users know it by, or empty when none does.
	Manager string
	// Error is why the last attempt failed, on one line.
	Error string
}

// upgradeCommand is the command that tries an upgrade now, and says
// why one cannot happen when it cannot.
const upgradeCommand = "trajector upgrade"

// autoUpgradesLabel opens every line about automatic upgrades, on
// status and in doctor alike.
const autoUpgradesLabel = "Automatic upgrades: "

// failed reports a last check that could not finish while automatic
// upgrades are on: the one state that asks the user for something.
func (st AutoUpgradeState) failed() bool {
	return st.On() && st.StateErr == nil && st.Result == selfupdate.ResultFailed
}

// On reports that this device checks on its own: a release build whose
// user config can be read and does not turn the check off, on a device
// whose accepted agreement tells the user about the check. It is the
// one test of that, for the session hook that starts the check as for
// every line that says whether it runs.
func (st AutoUpgradeState) On() bool {
	return !st.NotRelease && st.ConfigErr == nil && !st.Off && !st.NeedsAgreement
}

// stateUnreadable reports a state file that could not be read while
// the check is on, the one case where that file matters.
func (st AutoUpgradeState) stateUnreadable() bool {
	return st.On() && st.StateErr != nil
}

// failedHeadline is what failed: when, and that it did.
func (st AutoUpgradeState) failedHeadline() string {
	return autoUpgradesLabel + "the last attempt at " + st.CheckedAt.UTC().Format(time.RFC3339) + " failed"
}

// failedReason is why it failed, as a clause without its period.
func (st AutoUpgradeState) failedReason() string { return trimPeriod(st.Error) }

// failedSentence is the failure as status says it: what, why, and the
// command to run.
func (st AutoUpgradeState) failedSentence() string {
	line := st.failedHeadline()
	if reason := st.failedReason(); reason != "" {
		line += ": " + reason
	}
	return line + ". Run `" + upgradeCommand + "`."
}

// stateUnreadableSentence names a state file that could not be read.
// The next check writes the file afresh, so it asks nothing of the
// user, but what the last check found is unknown until then.
func (st AutoUpgradeState) stateUnreadableSentence() string {
	return fmt.Sprintf("%son; what the last check found could not be read: %v.", autoUpgradesLabel, st.StateErr)
}

// sentence is every state but a failure and an unreadable state file,
// as one line.
func (st AutoUpgradeState) sentence() string {
	switch {
	case st.NotRelease:
		return autoUpgradesLabel + "off (not a release build)."
	case st.ConfigErr != nil:
		return autoUpgradesLabel + "off (" + userdirs.ConfigFileName + " could not be read)."
	case st.Off:
		return autoUpgradesLabel + "off (" + userdirs.ConfigFileName + ")."
	case st.NeedsAgreement:
		return autoUpgradesLabel + "off until you accept the current data agreement; run `trajector enable` " + routing.ReEnableCaveat + "."
	case st.CheckedAt.IsZero():
		return autoUpgradesLabel + "on; no check has run yet."
	}
	checked := autoUpgradesLabel + "last checked " + st.CheckedAt.UTC().Format(time.RFC3339)
	switch st.Result {
	case selfupdate.ResultNewest:
		return checked + ", up to date."
	case selfupdate.ResultUpgraded:
		return checked + ", upgraded to " + st.Installed + "."
	case selfupdate.ResultManaged:
		return checked + ", managed by " + cmp.Or(st.Manager, "a package manager") + "."
	default:
		// A result a newer build wrote: when the check ran is still
		// known, and nothing here can say more without guessing.
		return checked + "."
	}
}

// checked reports a state whose last check came to something other
// than a failure, the one state doctor counts as a check that passed.
func (st AutoUpgradeState) checked() bool {
	return st.On() && st.StateErr == nil && !st.CheckedAt.IsZero() && !st.failed()
}

// autoUpgradesLine is the line status keeps in its Device section. A
// failure is marked but stops nothing from recording, so it never
// counts toward the exit code.
func autoUpgradesLine(s *section, st AutoUpgradeState) {
	switch {
	case st.failed():
		s.warnf("%s", st.failedSentence())
	case st.stateUnreadable():
		s.warnf("%s", st.stateUnreadableSentence())
	default:
		s.linef("%s", st.sentence())
	}
}

// doctorAutoUpgrades states the same as autoUpgradesLine in a doctor
// run, where a failure is a problem with its why and its fix apart.
func doctorAutoUpgrades(f *Findings, st AutoUpgradeState) {
	switch {
	case st.failed():
		f.ProblemFix(lowerFirst(st.failedHeadline()), st.failedReason(), upgradeCommand)
	case st.stateUnreadable():
		f.warnf("%s", lowerFirst(trimPeriod(st.stateUnreadableSentence())))
	case st.checked():
		f.OKf("%s", lowerFirst(trimPeriod(st.sentence())))
	default:
		f.notef("%s", lowerFirst(trimPeriod(st.sentence())))
	}
}

// lowerFirst writes a status line the way doctor writes its findings:
// starting in lower case.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
