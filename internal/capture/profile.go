// Package capture decides which calls are recorded and reassembles
// streamed responses into their non-streaming equivalent. v1 speaks the
// anthropic dialect only; a second provider would arrive as a sibling
// package, not as configuration of this one.
package capture

// Provider names the API dialect this package understands.
const Provider = "anthropic"

// OfficialUpstream is the provider's own API origin. Traffic routed
// anywhere else is third-party origin.
const OfficialUpstream = "https://api.anthropic.com"

// recordedEndpoints lists the exact request paths that are recorded.
// Matching is deliberately exact: sub-paths such as
// /v1/messages/count_tokens must fall through to plain forwarding.
var recordedEndpoints = map[string]bool{"/v1/messages": true}

// ShouldRecord reports whether a request is eligible for recording.
func ShouldRecord(method, path string) bool {
	return method == "POST" && recordedEndpoints[path]
}
