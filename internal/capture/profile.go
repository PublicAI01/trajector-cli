// Package capture decides which calls are recorded and reassembles
// streamed responses into their non-streaming equivalent. v1 speaks the
// anthropic dialect only; a second provider would arrive as a sibling
// Dialect value, not as configuration of this one.
package capture

import "encoding/json"

// Dialect is one provider's capture profile: identity, call
// eligibility, official origin, and stream reassembly. The pieces
// travel as one value so a proxy can never mix two providers' rules.
type Dialect struct {
	// Provider names the API dialect in stored rawcalls.
	Provider string
	// OfficialUpstream is the provider's own API origin. Traffic routed
	// anywhere else is third-party origin.
	OfficialUpstream string
	// ShouldRecord reports whether a request is eligible for recording.
	ShouldRecord func(method, path string) bool
	// Assemble reassembles a complete event stream into the equivalent
	// non-streaming object; AssemblyRules names the rules it applies.
	Assemble      func(stream []byte) (json.RawMessage, error)
	AssemblyRules string
}

// Anthropic is the dialect this build captures.
var Anthropic = Dialect{
	Provider:         "anthropic",
	OfficialUpstream: "https://api.anthropic.com",
	ShouldRecord:     shouldRecord,
	Assemble:         Assemble,
	AssemblyRules:    assemblyRulesVersion,
}

// recordedEndpoints lists the exact request paths that are recorded.
// Matching is deliberately exact: sub-paths such as
// /v1/messages/count_tokens must fall through to plain forwarding.
var recordedEndpoints = map[string]bool{"/v1/messages": true}

func shouldRecord(method, path string) bool {
	return method == "POST" && recordedEndpoints[path]
}
