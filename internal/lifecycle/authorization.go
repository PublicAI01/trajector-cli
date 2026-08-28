package lifecycle

import "strings"

// authorizeHint is the sentence every surface prints when the service
// has refused this account's uploads for want of a completed data
// authorization. One spelling, so a user who reads it in `upload`,
// `status` and `doctor` is being told to do one thing.
//
// It names an address only when the service supplied one. The client
// never composes the address itself from an origin it knows: that would
// freeze the page's location on the service's side forever, and a client
// already in the field could not be told it had moved. Without one the
// sentence still says what to do and where — the dashboard — which is
// enough to act on.
func authorizeHint(url string) string {
	if url == "" {
		return "Complete your data authorization in the Trajector dashboard, then uploads resume."
	}
	return "Complete your data authorization at " + url + " — then uploads resume."
}

// The refusal itself and the relay prefix are read on the same three
// surfaces as authorizeHint, so they are spelled once too. serviceSays
// also fronts the upgrade message: it is the one prefix marking words
// as the service's, not this client's.
const (
	authorizationPaused = "Uploads are paused: this account's data authorization is not complete."
	serviceSays         = "The service says: %s"
)

// doctorClause lowers a sentence into doctor's report style — a bare
// lowercase clause — so doctor shares a spelling instead of keeping a
// second copy of the words.
func doctorClause(sentence string) string {
	return strings.ToLower(sentence[:1]) + strings.TrimSuffix(sentence[1:], ".")
}
