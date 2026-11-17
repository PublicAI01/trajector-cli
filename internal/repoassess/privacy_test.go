package repoassess_test

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

// jsonNames lists the JSON name of every field a value of t carries,
// through nested structs and slices of them.
func jsonNames(t reflect.Type) []string {
	for t.Kind() == reflect.Slice || t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.PkgPath() == "time" {
		return nil
	}
	var names []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
		names = append(names, jsonNames(f.Type)...)
	}
	return names
}

func TestPrivacyNamesEverySubmissionField(t *testing.T) {
	privacy, err := os.ReadFile("../../PRIVACY.md")
	if err != nil {
		t.Fatal(err)
	}
	names := jsonNames(reflect.TypeFor[repoassess.Submission]())
	if len(names) < 50 {
		t.Fatalf("found only %d fields; the walk over the submission type is broken", len(names))
	}
	for _, name := range names {
		if !bytes.Contains(privacy, []byte("`"+name+"`")) {
			t.Errorf("PRIVACY.md does not name the submission field %q", name)
		}
	}
}
