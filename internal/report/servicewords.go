package report

import (
	"fmt"
	"strings"
)

// serviceSays is the one mark that words are the service's own rather
// than this client's.
const serviceSays = "The service says: %s"

// ServiceWords marks a message as the service's own. `upload`, `status`
// and `doctor` all relay one, so the mark is spelled once and no
// surface fills it in itself.
func ServiceWords(message string) string {
	return fmt.Sprintf(serviceSays, message)
}

// doctorClause lowers a sentence into doctor's report style — a bare
// lowercase clause — so doctor shares a spelling with the surfaces that
// print the sentence whole instead of keeping a second copy of the
// words.
func doctorClause(sentence string) string {
	return strings.ToLower(sentence[:1]) + strings.TrimSuffix(sentence[1:], ".")
}
