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
	masked, _, err := spliceFieldAware(s, parsed, nil, nil)
	return masked, err
}

// ReadingsOfOneScan is redactReadings: both readings of s from one scan.
func ReadingsOfOneScan(s string) (redacted, deterministic string) {
	return redactReadings(s)
}

// ReadingsOfTwoScans gives the same two readings of s from a separate
// scan each, the full one and the one without layers 1 and 7.
func ReadingsOfTwoScans(s string) (redacted, deterministic string) {
	return applyRegions(s, detectLayers(s, true)), applyRegions(s, detectLayers(s, false))
}

// MaskWithoutMemo is Masker.JSONLBytes with no memo: every string value
// is scanned where it occurs.
func MaskWithoutMemo(b []byte) (RedactedBytes, error) {
	return jsonlBytes(b, nil)
}
