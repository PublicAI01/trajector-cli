package report

import "strings"

// ServiceSays is the one prefix marking words as the service's own
// rather than this client's. `upload`, `status` and `doctor` all relay
// them, so the mark is spelled once.
const ServiceSays = "The service says: %s"

// doctorClause lowers a sentence into doctor's report style — a bare
// lowercase clause — so doctor shares a spelling with the surfaces that
// print the sentence whole instead of keeping a second copy of the
// words.
func doctorClause(sentence string) string {
	return strings.ToLower(sentence[:1]) + strings.TrimSuffix(sentence[1:], ".")
}
