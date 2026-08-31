package claudesettings

// KeyShowThinkingSummaries is the Claude Code setting that controls
// whether thinking summaries are shown to the user.
const KeyShowThinkingSummaries = "showThinkingSummaries"

// Disclosure is one labeled line of what the user is told before
// deciding about an optional setting.
type Disclosure struct {
	Label string
	Text  string
}

// OptionalSetting describes a Claude Code setting trajector may ask
// the user to change. Every string is final wording: rendering may
// re-wrap lines, never rewrite words — this text is the whole
// disclosure the user gets, with nothing elsewhere to back it up.
type OptionalSetting struct {
	Key string
	// Target is the value written when the user accepts.
	Target      bool
	Intro       string
	Disclosures []Disclosure
	// Fact is stated after the disclosures.
	Fact string
	// AlreadyOn is stated when the user already has the setting on
	// themselves: what their own configuration already achieves.
	AlreadyOn string
}

// OptionalSettings is the one truth source for the settings trajector
// may ask about.
var OptionalSettings = []OptionalSetting{{
	Key:    KeyShowThinkingSummaries,
	Target: true,
	Intro: "Claude Code can show you a summary of the model's reasoning as it " +
		"works. It is off by default, and turning it on also makes the " +
		"records you contribute more complete.",
	Disclosures: []Disclosure{
		{"What changes", KeyShowThinkingSummaries + " becomes true in " + ProjectLocalRel},
		{"For you", "you see the model's reasoning summarised as it works"},
		{"For us", "your contributed records carry that reasoning instead of " +
			"an empty field, which makes them more valuable to us"},
		{"How far", "this project only; no other project on this machine is affected"},
		{"Cost", "none. Reasoning is generated and billed either way — this " +
			"only decides whether you are shown it"},
		{"Undoing it", "`trajector disable` puts it back exactly as it was, or " +
			"change it yourself at any time"},
		{"If you say no", "nothing else changes. Recording, rewards, and every " +
			"other part of trajector work the same"},
	},
	Fact: "Claude Code stopped generating these summaries by default in " +
		"v2.1.89. This setting turns them back on.",
	AlreadyOn: "Your contributed records already carry the model's reasoning.",
}}
