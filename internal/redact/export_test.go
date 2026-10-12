package redact

import (
	"encoding/json"
	"strings"
)

// HasDuplicateJSONKeys is hasDuplicateJSONKeys, for the tests outside
// the package.
var HasDuplicateJSONKeys = hasDuplicateJSONKeys

// SpliceJSON runs the field-aware pass alone over s, which must be one
// JSON value: the values it collects from the decoded tree are replaced
// in the raw text, with no other decision taken first.
func SpliceJSON(s string) (string, error) {
	var parsed any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &parsed); err != nil {
		return "", err
	}
	return applyJSONReplacements(s, collectJSONLReplacements(parsed))
}
