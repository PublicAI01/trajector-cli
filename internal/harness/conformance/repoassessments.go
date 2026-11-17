package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// RepoAssessmentCase is one fixture of the repository assessment
// endpoint: the request body a client sends, when the case has one, and
// the service's answer.
type RepoAssessmentCase struct {
	// Name is the fixture's directory name, used in test output.
	Name     string
	Meta     RepoAssessmentMeta
	Response Response
	// Request is the request body before compression, verbatim. It is
	// nil for a case that has only a response.
	Request []byte
}

// RepoAssessmentMeta is the fixture's own statement of what it covers.
type RepoAssessmentMeta struct {
	Name        string `json:"name"`
	ContractRef string `json:"contract_ref"`
	// Expect decodes straight into the client's own outcome type, for
	// the reason Meta.Expect does.
	Expect     platform.RepoAssessmentOutcome `json:"expect"`
	ServerWant string                         `json:"server_expect,omitempty"`
	// ByteExact says the client, given the same submission, serializes
	// exactly Request.
	ByteExact bool `json:"byte_exact,omitempty"`
	// UnknownKeys and OutOfBounds are JSON Pointers into Request: keys
	// the contract does not have, and paths a client never sends.
	UnknownKeys []string `json:"unknown_keys,omitempty"`
	OutOfBounds []string `json:"out_of_bounds,omitempty"`
}

// LoadRepoAssessments reads every repository assessment case under dir,
// in name order. Like LoadBlocks, a fixture set without the directory
// is an error.
func LoadRepoAssessments(dir string) ([]RepoAssessmentCase, error) {
	root := filepath.Join(dir, "repo-assessments")
	names, err := caseDirs(root)
	if err != nil {
		return nil, err
	}
	var cases []RepoAssessmentCase
	for _, name := range names {
		base := filepath.Join(root, name)
		c := RepoAssessmentCase{Name: name}
		if err := readJSON(filepath.Join(base, caseFile), &c.Meta); err != nil {
			return nil, err
		}
		if err := readJSON(filepath.Join(base, "response.json"), &c.Response); err != nil {
			return nil, err
		}
		c.Request, err = os.ReadFile(filepath.Join(base, "request.json"))
		if errors.Is(err, fs.ErrNotExist) {
			c.Request = nil
		} else if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// RepoAssessmentStringViolations lists every string in a repository
// assessment body that breaks the contract's rules for strings: apart
// from a commit message, no string starts with "/", "\" or a drive
// letter, or holds "://"; apart from a message and a path, no string
// holds "@"; and no path has a ".." segment. Each entry names the
// string by its JSON Pointer.
func RepoAssessmentStringViolations(body []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var bad []string
	var walk func(pointer string, key string, v any)
	walk = func(pointer, key string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				walk(pointer+"/"+k, k, v[k])
			}
		case []any:
			for i, e := range v {
				walk(fmt.Sprintf("%s/%d", pointer, i), key, e)
			}
		case string:
			if why := stringViolation(key, v); why != "" {
				bad = append(bad, pointer+": "+why)
			}
		}
	}
	walk("", "", v)
	return bad, nil
}

func stringViolation(key, s string) string {
	if key == "message" {
		return ""
	}
	switch {
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, `\`):
		return "starts like an absolute path"
	case len(s) >= 2 && s[1] == ':' && ('A' <= s[0] && s[0] <= 'Z' || 'a' <= s[0] && s[0] <= 'z'):
		return "starts with a drive letter"
	case strings.Contains(s, "://"):
		return "holds a URL"
	case key != "path" && strings.Contains(s, "@"):
		return "holds an @"
	case key == "path" && slices.Contains(strings.Split(s, "/"), ".."):
		return "has a .. segment"
	}
	return ""
}
