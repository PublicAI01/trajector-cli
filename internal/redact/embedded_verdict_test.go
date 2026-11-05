package redact_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// The password is low entropy on purpose: a random-looking one is caught
// by the entropy layer and would prove nothing about the key verdict
// reaching an array element across a document boundary.
const embeddedVerdictPassword = "hunter2hunter"

func embeddedVerdictDocument(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertOneTokenForBothCopies(t testing.TB, result string) {
	t.Helper()
	if strings.Contains(result, embeddedVerdictPassword) {
		t.Fatalf("the password survived somewhere in %s", result)
	}
	if got := strings.Count(result, `REDACTED`); got < 2 {
		t.Fatalf("expected both copies to carry the same REDACTED token, found %d in %s", got, result)
	}
	if !strings.Contains(result, `--pw`) {
		t.Fatalf("masking swallowed the neighbouring argument: %s", result)
	}
	var parsed any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("redacted output is not valid JSON: %v\n%s", err, result)
	}
}

func TestJSONLBytes_CredentialKeyInsideAnEmbeddedDocumentReachesAnOuterArrayElement(t *testing.T) {
	t.Parallel()
	inner := embeddedVerdictDocument(t, map[string]any{"password": embeddedVerdictPassword, "host": "db.internal", "user": "svc"})
	input := embeddedVerdictDocument(t, map[string]any{
		"argv":       []any{"--pw", embeddedVerdictPassword},
		"tool_input": inner,
	})

	assertOneTokenForBothCopies(t, redactedString(t, input))
}

func TestJSONLBytes_CredentialKeyOutsideAnEmbeddedDocumentReachesAnArrayElementInsideIt(t *testing.T) {
	t.Parallel()
	inner := embeddedVerdictDocument(t, map[string]any{"argv": []any{"--pw", embeddedVerdictPassword}})
	input := embeddedVerdictDocument(t, map[string]any{
		"db":         map[string]any{"password": embeddedVerdictPassword, "host": "db.internal", "user": "svc"},
		"tool_input": inner,
	})

	assertOneTokenForBothCopies(t, redactedString(t, input))
}

func TestJSONLBytes_CredentialKeyVerdictCrossesTwoDocumentBoundariesEachWay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input func(t testing.TB) string
	}{
		{
			name: "VerdictTwoDocumentsDownElementAtTheTop",
			input: func(t testing.TB) string {
				t.Helper()
				innermost := embeddedVerdictDocument(t, map[string]any{"password": embeddedVerdictPassword, "host": "db.internal", "user": "svc"})
				middle := embeddedVerdictDocument(t, map[string]any{"result": innermost})
				return embeddedVerdictDocument(t, map[string]any{
					"argv":       []any{"--pw", embeddedVerdictPassword},
					"tool_input": middle,
				})
			},
		},
		{
			name: "VerdictAtTheTopElementTwoDocumentsDown",
			input: func(t testing.TB) string {
				t.Helper()
				innermost := embeddedVerdictDocument(t, map[string]any{"argv": []any{"--pw", embeddedVerdictPassword}})
				middle := embeddedVerdictDocument(t, map[string]any{"result": innermost})
				return embeddedVerdictDocument(t, map[string]any{
					"db":         map[string]any{"password": embeddedVerdictPassword, "host": "db.internal", "user": "svc"},
					"tool_input": middle,
				})
			},
		},
		{
			name: "VerdictInTheMiddleDocumentElementsAboveAndBelow",
			input: func(t testing.TB) string {
				t.Helper()
				innermost := embeddedVerdictDocument(t, map[string]any{"argv": []any{"--pw", embeddedVerdictPassword}})
				middle := embeddedVerdictDocument(t, map[string]any{
					"db":     map[string]any{"password": embeddedVerdictPassword, "host": "db.internal", "user": "svc"},
					"result": innermost,
				})
				return embeddedVerdictDocument(t, map[string]any{
					"args":       []any{"--pw", embeddedVerdictPassword},
					"tool_input": middle,
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := tc.input(t)
			assertOneTokenForBothCopies(t, redactedString(t, input))
		})
	}
}

func TestJSONLBytes_EmbeddedDocumentVerdictDoesNotReachAKeyedFieldOrProse(t *testing.T) {
	t.Parallel()
	inner := embeddedVerdictDocument(t, map[string]any{"password": embeddedVerdictPassword, "host": "db.internal", "user": "svc"})
	input := embeddedVerdictDocument(t, map[string]any{
		"note":       "remember " + embeddedVerdictPassword + " for later",
		"title":      embeddedVerdictPassword,
		"tool_input": inner,
	})

	result := redactedString(t, input)
	for _, kept := range []string{
		`"note":"remember ` + embeddedVerdictPassword + ` for later"`,
		`"title":"` + embeddedVerdictPassword + `"`,
	} {
		if !strings.Contains(result, kept) {
			t.Errorf("a field judged on its own key was overruled by a verdict from inside a document: want %s in %s", kept, result)
		}
	}
	if strings.Contains(result, `\"password\":\"`+embeddedVerdictPassword) {
		t.Errorf("the credential inside the document was left in the clear: %s", result)
	}
}

func TestJSONLBytes_EmbeddedDocumentVerdictMasksTheSameWithAndWithoutAMemo(t *testing.T) {
	t.Parallel()
	inner := embeddedVerdictDocument(t, map[string]any{"password": embeddedVerdictPassword, "host": "db.internal", "user": "svc"})
	shared := embeddedVerdictDocument(t, map[string]any{"argv": []any{"--pw", embeddedVerdictPassword}})
	records := []string{
		embeddedVerdictDocument(t, map[string]any{"tool_input": shared, "messages": []any{"hello"}}),
		embeddedVerdictDocument(t, map[string]any{"tool_input": shared, "db": map[string]any{"password": embeddedVerdictPassword, "host": "h", "user": "u"}}),
		embeddedVerdictDocument(t, map[string]any{"tool_input": shared, "messages": []any{"hello"}}),
		embeddedVerdictDocument(t, map[string]any{"tool_input": inner, "argv": []any{"--pw", embeddedVerdictPassword}}),
		embeddedVerdictDocument(t, map[string]any{"tool_input": inner}),
	}

	m := redact.NewMasker()
	for i, record := range records {
		got, err := m.JSONLBytes([]byte(record))
		if err != nil {
			t.Fatal(err)
		}
		want, err := redact.MaskWithoutMemo([]byte(record))
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Bytes()) != string(want.Bytes()) {
			t.Errorf("record %d: masked with the memo differs from masked without it:\n got %s\nwant %s", i, got.Bytes(), want.Bytes())
		}
	}
}

func TestJSONLBytes_CredentialKeyOutsideReachesAnElementTheDocumentSpellsWithEscapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		password string
		escaped  rune
	}{
		{name: "PlusEscaped", password: "ab+cd1234", escaped: '+'},
		{name: "BacktickEscaped", password: "ab`cd1234", escaped: '`'},
		{name: "PlusLiteral", password: "ab+cd1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spelling := tc.password
			if tc.escaped != 0 {
				spelling = strings.ReplaceAll(spelling, string(tc.escaped), fmt.Sprintf("%cu%04X", '\\', tc.escaped))
			}
			inner := `{"argv":["--pw","` + spelling + `"]}`
			input := embeddedVerdictDocument(t, map[string]any{
				"db":         map[string]any{"password": tc.password, "host": "db.internal", "user": "svc"},
				"tool_input": inner,
			})

			result := redactedString(t, input)
			if strings.Contains(result, "cd1234") {
				t.Fatalf("the password survived somewhere in %s", result)
			}
			if got := strings.Count(result, `REDACTED`); got < 2 {
				t.Fatalf("expected both copies to be masked, found %d REDACTED in %s", got, result)
			}
			if !strings.Contains(result, `--pw`) {
				t.Fatalf("masking swallowed the neighbouring argument: %s", result)
			}
		})
	}
}
