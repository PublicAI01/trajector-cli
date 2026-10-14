package claudesettings

// KeyShowThinkingSummaries is the Claude Code setting that controls
// whether thinking summaries are shown to the user.
const KeyShowThinkingSummaries = "showThinkingSummaries"

// reasoningSummariesDoc is the public page that says what counts as a
// session with reasoning summaries and how each Claude Code surface
// turns them on. Every sentence about leaving showThinkingSummaries off
// points there instead of restating it.
const reasoningSummariesDoc = "https://docs.publicai.io/publicai-documentation/publicai-trajector-cli/reasoning-summaries"

// ReasoningSummariesOffCost is what leaving showThinkingSummaries off
// costs, as a lowercase clause each surface builds its own sentence
// around.
const ReasoningSummariesOffCost = "sessions without reasoning summaries are not rewarded"

// Disclosure is one labeled line of what the user is told before
// deciding about a requested setting.
type Disclosure struct {
	Label string
	Text  string
}

// RequestedSetting describes a Claude Code setting trajector asks the
// user to change. Every string is final wording: rendering may re-wrap
// lines, never rewrite words — this text is the whole disclosure the
// user gets, with nothing elsewhere to back it up.
type RequestedSetting struct {
	Key string
	// Target is the value written when the user accepts.
	Target bool
	// Required marks a setting trajector asks for as a condition of
	// contributing: its disclosures state what leaving it off costs,
	// and one refusal does not end the ask. A setting that is not
	// required changes nothing else when the user leaves it off, and
	// one refusal ends it.
	Required bool
	// OffCost is what leaving a required setting off costs, as a
	// lowercase clause; empty for a setting that is not required.
	OffCost     string
	Intro       string
	Disclosures []Disclosure
	// Fact is stated after the disclosures.
	Fact string
	// AlreadyOn is stated when the user already has the setting on
	// themselves: what their own configuration already achieves.
	AlreadyOn string
}

// RequestedSettings is the one truth source for the settings trajector
// asks the user to change.
var RequestedSettings = []RequestedSetting{{
	Key:      KeyShowThinkingSummaries,
	Target:   true,
	Required: true,
	OffCost:  ReasoningSummariesOffCost,
	Intro: "Claude Code can show you a summary of the model's reasoning as it " +
		"works. It is off by default, and turning it on also makes the " +
		"records you contribute more complete.",
	Disclosures: []Disclosure{
		{"What changes", KeyShowThinkingSummaries + " becomes true in " + ProjectLocalRel},
		{"For you", "you see the model's reasoning summarised as it works"},
		{"For us", "your contributed records carry that reasoning instead of " +
			"an empty field, which makes them more valuable to us"},
		{"How far", "this project only; no other project on this machine is affected"},
		{"Token cost", "none. Reasoning is generated and billed either way — this " +
			"only decides whether you are shown it"},
		{"Undoing it", "`trajector disable` puts it back exactly as it was, or " +
			"change it yourself at any time"},
		{"If you say no", "recording goes on, but " + ReasoningSummariesOffCost +
			". The current rule: " + reasoningSummariesDoc},
		{"Where it works", "interactive Claude Code in a terminal. claude -p, the " +
			"desktop app and the SDK have their own switch: " + reasoningSummariesDoc},
	},
	Fact: "Claude Code stopped generating these summaries by default in " +
		"v2.1.89. This setting turns them back on.",
	AlreadyOn: "Your contributed records already carry the model's reasoning.",
}}
