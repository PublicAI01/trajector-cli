package lifecycle

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/report"
)

// offerOptionalSettings puts each optional Claude Code setting to the
// user according to its classified state, writes the ones accepted, and
// records every answer. It runs after the project's granted state is
// recorded — a setting decision has no record to attach to before that
// — and before injection. It never returns an error: a failure to ask,
// write, or record degrades to "nothing changed", because the optional
// ask must never break enable. Every question changes the file only on
// an explicit yes: the suggested default and an answer of no both leave
// it untouched, whichever way the question points.
func (m *Machine) offerOptionalSettings(io IO, st report.ProjectStatus) {
	// A decision store that cannot be read leaves writtenByUs unknowable;
	// every true then classifies as the user's own, which disable will
	// never delete. Failing toward not touching the user's value.
	decisions, err := m.consent.SettingDecisions(st.Hash)
	if err != nil {
		decisions = nil
	}
	for _, s := range claudesettings.OptionalSettings {
		d, recorded := decisions[s.Key]
		status := claudesettings.ClassifySetting(st.Root, m.deps.Home, s.Key, recorded && wroteSetting(d))
		fmt.Fprintf(io.Out, "\nOptional setting for this project\n\n")
		switch status.State {
		case claudesettings.OnByUser:
			printWrapped(io.Out, statementIndent, fmt.Sprintf(
				"%s is already true in your %s. Nothing to change. %s",
				s.Key, status.Source, s.AlreadyOn))
		case claudesettings.OnByUs:
			printWrapped(io.Out, statementIndent, fmt.Sprintf(
				"%s is on for this project; trajector set it when you enabled. `trajector disable` puts it back.", s.Key))
			fmt.Fprintln(io.Out)
			off, answered := askSetting(io, "Turn it off? [y/N] ", false)
			if !answered {
				printNeedsInteractive(io)
				return
			}
			if off {
				m.turnOffOurSetting(io, st, s)
			}
		case claudesettings.OffByUser:
			printWrapped(io.Out, statementIndent, fmt.Sprintf("You have %s set to false in your %s.", s.Key, status.Source))
			fmt.Fprintln(io.Out)
			printWrapped(io.Out, statementIndent, fmt.Sprintf(
				"Confirming would set it to %v for this project only, overriding that. "+
					"You can leave it as false and nothing else changes — recording, "+
					"rewards, and every other part of trajector work the same.", s.Target))
			fmt.Fprintln(io.Out)
			printDisclosures(io.Out, s.Disclosures)
			fmt.Fprintln(io.Out)
			yes, answered := askSetting(io, "Turn it on for this project? [y/N] ", false)
			if !answered {
				printNeedsInteractive(io)
				return
			}
			m.applySettingAnswer(io, st, s, yes)
		default: // Unset
			printWrapped(io.Out, statementIndent, s.Intro)
			fmt.Fprintln(io.Out)
			printDisclosures(io.Out, s.Disclosures)
			fmt.Fprintln(io.Out)
			printWrapped(io.Out, statementIndent, s.Fact)
			fmt.Fprintln(io.Out)
			yes, answered := askSetting(io, "Turn it on? [Y/n] ", true)
			if !answered {
				printNeedsInteractive(io)
				return
			}
			m.applySettingAnswer(io, st, s, yes)
		}
	}
}

// optionalSettingStatuses classifies every optional setting for the
// diagnosis status renders. A decision store that cannot be read reads
// as no decisions, exactly as when enable asks: every true then
// classifies as the user's own.
func (m *Machine) optionalSettingStatuses(st report.ProjectStatus) []report.OptionalSettingStatus {
	decisions, err := m.consent.SettingDecisions(st.Hash)
	if err != nil {
		decisions = nil
	}
	statuses := make([]report.OptionalSettingStatus, 0, len(claudesettings.OptionalSettings))
	for _, s := range claudesettings.OptionalSettings {
		d, recorded := decisions[s.Key]
		classified := claudesettings.ClassifySetting(st.Root, m.deps.Home, s.Key, recorded && wroteSetting(d))
		statuses = append(statuses, report.OptionalSettingStatus{
			Key:      s.Key,
			State:    classified.State,
			Declined: recorded && d.Answer == consent.AnswerDeclined,
		})
	}
	return statuses
}

// applySettingAnswer records one answered question and carries an
// acceptance out. Record before write: a write without its record could
// never be undone — disable consults only the record — while a record
// without its write restores nothing, because RestoreTopLevelBool acts
// only when the current value is the one trajector wrote. The failure
// that can land between the two is the harmless one in this order.
func (m *Machine) applySettingAnswer(io IO, st report.ProjectStatus, s claudesettings.OptionalSetting, accepted bool) {
	if !accepted {
		if err := m.consent.SetSettingDecision(st.Hash, s.Key, consent.SettingDecision{
			Answer:    consent.AnswerDeclined,
			DecidedAt: m.now(),
		}); err != nil {
			fmt.Fprintf(io.Err, "trajector: WARNING: could not record your answer for %s: %v\n", s.Key, err)
		}
		return
	}
	prior := consent.PriorAbsent
	if value, found := claudesettings.TopLevelBool(st.SettingsPath(), s.Key); found {
		prior = consent.PriorFalse
		if value {
			prior = consent.PriorTrue
		}
	}
	if err := m.consent.SetSettingDecision(st.Hash, s.Key, consent.SettingDecision{
		Answer:    consent.AnswerAccepted,
		Prior:     prior,
		DecidedAt: m.now(),
	}); err != nil {
		fmt.Fprintf(io.Err, "trajector: WARNING: could not turn on %s; nothing was changed: %v\n", s.Key, err)
		return
	}
	if prior == consent.PriorTrue {
		// Already holds the value the user just accepted; writing nothing
		// keeps it theirs, and the recorded prior keeps disable's hands off.
		return
	}
	if err := claudesettings.SetTopLevelBool(st.SettingsPath(), s.Key, s.Target); err != nil {
		// The record just made claims a write that did not happen; taking
		// it back keeps a rerun asking instead of standing on a stale claim.
		_ = m.consent.ClearSettingDecision(st.Hash, s.Key)
		fmt.Fprintf(io.Err, "trajector: WARNING: could not turn on %s; nothing was changed: %v\n", s.Key, err)
	}
}

// turnOffOurSetting is the enable-side undo: an OnByUs setting the user
// just asked to turn off goes back through the same restore path
// disable uses.
func (m *Machine) turnOffOurSetting(io IO, st report.ProjectStatus, s claudesettings.OptionalSetting) {
	undone, failures := m.restoreRecordedSettingKey(st.Root, st.Hash, s)
	for _, key := range undone {
		fmt.Fprintf(io.Out, "  %s\n", settingRestoredLine(key))
	}
	for _, err := range failures {
		fmt.Fprintf(io.Err, "trajector: WARNING: %v\n", err)
	}
}

// wroteSetting reports whether a recorded decision marks a write of
// trajector's own: accepted, with a prior the write changed. An
// acceptance that found the value already in place wrote nothing, so it
// never counts — counting it is how disable would come to delete a
// value the user set.
func wroteSetting(d consent.SettingDecision) bool {
	return d.Answer == consent.AnswerAccepted &&
		(d.Prior == consent.PriorAbsent || d.Prior == consent.PriorFalse)
}

// restoreRecordedSettings undoes what a project's recorded setting
// decisions say trajector wrote, for every surface that removes what
// enable put in place: disable, uninstall, and doctor's stale-injection
// repair all call it next to removeInjection. Declined answers are left
// standing — one refusal keeps ending the ask across disable/enable
// cycles. A record is cleared only once its restore succeeded: the
// record is the only undo there is, so a failed restore must keep it.
func (m *Machine) restoreRecordedSettings(root, hash string) (undone []string, failures []error) {
	for _, s := range claudesettings.OptionalSettings {
		u, f := m.restoreRecordedSettingKey(root, hash, s)
		undone = append(undone, u...)
		failures = append(failures, f...)
	}
	return undone, failures
}

// restoreRecordedSettingKey applies the decision table to one accepted
// setting: only a prior of absent or false marks a write to take back,
// and the write comes back out only while the file still holds it — a
// later hand edit is the user's most recent decision and wins.
func (m *Machine) restoreRecordedSettingKey(root, hash string, s claudesettings.OptionalSetting) (undone []string, failures []error) {
	decisions, err := m.consent.SettingDecisions(hash)
	if err != nil {
		return nil, []error{fmt.Errorf("reading the recorded setting decisions: %w", err)}
	}
	d, ok := decisions[s.Key]
	if !ok || d.Answer != consent.AnswerAccepted {
		return nil, nil
	}
	path := claudesettings.ProjectLocalPath(root)
	acted := false
	if wroteSetting(d) {
		if current, found := claudesettings.TopLevelBool(path, s.Key); found && current == s.Target {
			acted = true
		}
		if err := claudesettings.RestoreTopLevelBool(path, s.Key, s.Target, false, d.Prior == consent.PriorFalse); err != nil {
			return nil, []error{fmt.Errorf(
				"could not set %s back to what it was before trajector wrote it: %v (the record was kept so a rerun can retry)",
				s.Key, err)}
		}
	}
	if err := m.consent.ClearSettingDecision(hash, s.Key); err != nil {
		return nil, []error{fmt.Errorf("could not clear the recorded decision for %s: %v (a rerun can retry)", s.Key, err)}
	}
	if acted {
		undone = append(undone, s.Key)
	}
	return undone, nil
}

// settingRestoredLine is the one sentence every surface prints when it
// puts a setting back, so disable, uninstall, and enable's own undo
// cannot describe the same act differently.
func settingRestoredLine(key string) string {
	return fmt.Sprintf("Set %s back to what it was before trajector wrote it.", key)
}

// reportRestoredSettings prints one removal's setting restores the way
// disable and uninstall report the rest of their work.
func reportRestoredSettings(io IO, undone []string, failures []error) {
	for _, key := range undone {
		fmt.Fprintln(io.Out, settingRestoredLine(key))
	}
	for _, err := range failures {
		fmt.Fprintf(io.Err, "trajector: WARNING: %v\n", err)
	}
}

// printNeedsInteractive says why nothing was asked and nothing changed.
// The condition is the one under which the agreement prompt cannot ask
// either: the read yields no answer at all, which is what a script, a
// pipeline, or a closed stdin look like — and none of the disclosure
// conditions can be met when nobody is reading.
func printNeedsInteractive(io IO) {
	fmt.Fprintln(io.Out, "Optional settings were not changed: they need an interactive session.")
	fmt.Fprintln(io.Out, "Run `trajector enable` from a terminal to review them.")
}

// askSetting puts one optional-setting question. Empty input takes the
// stated default; otherwise only an explicit yes answers true, as in
// askYesNo. answered is false when the read yields nothing at all.
func askSetting(io IO, prompt string, def bool) (yes, answered bool) {
	fmt.Fprint(io.Out, prompt)
	line, err := bufio.NewReader(io.In).ReadString('\n')
	if err != nil && line == "" {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, true
	case "yes", "y":
		return true, true
	}
	return false, true
}

// Rendering re-wraps the finalized disclosure wording; it never rewrites
// a word of it. The width matches the prototype the wording came from.
const (
	settingScreenWidth = 73
	statementIndent    = 2
	// disclosureGap separates the longest label from its text.
	disclosureGap = 3
)

// printWrapped prints text as one greedily wrapped, indented paragraph.
func printWrapped(w io.Writer, indent int, text string) {
	pad := strings.Repeat(" ", indent)
	for _, line := range wrapText(text, settingScreenWidth-indent) {
		fmt.Fprintf(w, "%s%s\n", pad, line)
	}
}

// printDisclosures lays the labeled rows out in two columns: labels
// left, text wrapped in a column starting past the longest label.
func printDisclosures(w io.Writer, disclosures []claudesettings.Disclosure) {
	width := 0
	for _, d := range disclosures {
		if n := utf8.RuneCountInString(d.Label); n > width {
			width = n
		}
	}
	textCol := statementIndent + width + disclosureGap
	for _, d := range disclosures {
		lines := wrapText(d.Text, settingScreenWidth-textCol)
		for i, line := range lines {
			if i == 0 {
				fmt.Fprintf(w, "%s%-*s%s\n", strings.Repeat(" ", statementIndent), width+disclosureGap, d.Label, line)
				continue
			}
			fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", textCol), line)
		}
	}
}

// wrapText greedily wraps text at width, measured in runes.
func wrapText(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
