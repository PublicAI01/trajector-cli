package redact_test

import (
	"encoding/base64"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// fakeImageContent is base64 of random bytes: the shape an image or a
// PDF has on a session line, and one the entropy layer masks in parts
// wherever it reads it.
func fakeImageContent() string {
	content := make([]byte, 3<<10)
	_, _ = rand.NewChaCha8([32]byte{1}).Read(content)
	return base64.StdEncoding.EncodeToString(content)
}

// readLine is a session line on which Read opened a file: the content
// in a block of the tool result, and again in the Read result at the
// root of the line. The prose and the Read result's caption each hold a
// secret.
func readLine(t *testing.T, resultType, content string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":      "user",
		"sessionId": fixtureSessionID,
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_01",
					"content": []any{map[string]any{
						"type":   "image",
						"source": map[string]any{"type": "base64", "media_type": "image/png", "data": content},
					}},
				},
				map[string]any{"type": "text", "text": "Use api_key=" + highEntropySecret + " for the next call."},
			},
		},
		"toolUseResult": map[string]any{
			"type": resultType,
			"file": map[string]any{
				"base64":       content,
				"type":         "image/png",
				"originalSize": 3 << 10,
				"caption":      "token " + highEntropySecret,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func TestSegmentKeepsTheContentOfAReadResultAndMasksTheRestOfTheLine(t *testing.T) {
	content := fakeImageContent()
	for _, resultType := range []string{"image", "pdf"} {
		t.Run(resultType, func(t *testing.T) {
			got := redactSegmentLines(t, readLine(t, resultType, content)+"\n")

			var line struct {
				ToolUseResult struct {
					File struct {
						Base64 string `json:"base64"`
					} `json:"file"`
				} `json:"toolUseResult"`
			}
			if err := json.Unmarshal([]byte(got), &line); err != nil {
				t.Fatalf("masked line is not JSON: %v", err)
			}
			if line.ToolUseResult.File.Base64 != content {
				t.Errorf("Read result content was rewritten: %d bytes left of %d", len(line.ToolUseResult.File.Base64), len(content))
			}
			if n := strings.Count(got, content); n != 2 {
				t.Errorf("content stands %d times on the masked line, want 2 (block and Read result)", n)
			}
			if strings.Contains(got, highEntropySecret) {
				t.Errorf("a secret elsewhere on the line was left in the clear: %s", got)
			}
		})
	}
}

func TestReadResultShapedContentIsMaskedWhereNoReadResultStands(t *testing.T) {
	content := fakeImageContent()
	notBase64 := strings.NewReplacer("+", "-", "/", "_").Replace(content)
	nested, err := json.Marshal(map[string]any{
		"type":      "user",
		"sessionId": fixtureSessionID,
		"data": map[string]any{
			"toolUseResult": map[string]any{"type": "image", "file": map[string]any{"base64": content, "type": "image/png"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		mask func(t *testing.T) string
	}{
		{"type is text", func(t *testing.T) string {
			t.Helper()
			return redactSegmentLines(t, readLine(t, "text", content)+"\n")
		}},
		{"type only starts with image", func(t *testing.T) string {
			t.Helper()
			return redactSegmentLines(t, readLine(t, "image_metadata", content)+"\n")
		}},
		{"content is not base64", func(t *testing.T) string {
			t.Helper()
			return redactSegmentLines(t, readLine(t, "image", notBase64)+"\n")
		}},
		{"not at the root of the line", func(t *testing.T) string {
			t.Helper()
			return redactSegmentLines(t, string(nested)+"\n")
		}},
		{"in a rawcall", func(t *testing.T) string {
			t.Helper()
			return redactedString(t, readLine(t, "image", content))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.mask(t)
			var line struct {
				ToolUseResult struct {
					File struct {
						Base64 string `json:"base64"`
					} `json:"file"`
				} `json:"toolUseResult"`
				Data struct {
					ToolUseResult struct {
						File struct {
							Base64 string `json:"base64"`
						} `json:"file"`
					} `json:"toolUseResult"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(got), &line); err != nil {
				t.Fatalf("masked line is not JSON: %v", err)
			}
			masked := line.ToolUseResult.File.Base64 + line.Data.ToolUseResult.File.Base64
			if masked == content || !strings.Contains(masked, "REDACTED") {
				t.Errorf("content was not masked: %.80s…", masked)
			}
		})
	}
}
