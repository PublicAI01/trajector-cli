// Package conformance loads the shared upload-contract fixtures — the
// same bytes the service side loads — so both sides of the contract are
// checked against one description of it rather than two.
//
// Each side has tests of its own built from fixtures of its own. Those
// cannot catch the failure that matters most here: both sides passing
// their own tests while disagreeing with each other. Only reading the
// same bytes can.
//
// The fixtures live outside this repository and are not part of it. When
// they are absent — which is the normal case for anyone who checked out
// only this repository — Find returns no directory and the tests that
// use it say so and skip. They must never pass quietly instead: a test
// that checks nothing and reports success is worse than no test.
package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// Disposition is the client's own verdict type. The fixtures and the
// uploader name one closed set between them, so a fixture's word is
// read as that type instead of being translated into it — a translation
// is where the two descriptions of the contract would drift apart.
type Disposition = upload.Disposition

// StrictEnv makes an absent fixture directory a failure instead of a
// skip. It is for the one environment where the fixtures are certain to
// be present; everywhere else their absence is expected.
const StrictEnv = "TRAJECTOR_L2_STRICT"

// DirEnv names the fixture directory explicitly, bypassing the search.
const DirEnv = "TRAJECTOR_CONFORMANCE_DIR"

// Case is one fixture: what the service answered, and what the client is
// required to do about it.
type Case struct {
	// Name is the fixture's directory name, used in test output.
	Name string
	Meta Meta
	// Response is the service's answer, verbatim from the fixture.
	Response Response
	// Envelope is the batch envelope the fixture describes. The client
	// builds its own envelopes, so this is read to check the shape both
	// sides agree on, not to send.
	Envelope map[string]any
}

// Meta is the fixture's own statement of what it covers.
type Meta struct {
	Name        string `json:"name"`
	ContractRef string `json:"contract_ref"`
	// Expect names the disposition the contract requires of the client.
	// It decodes straight into the production type: the fixture's word
	// and the client's value are the same closed set, so a fixture
	// naming a disposition this client does not implement fails to match
	// any of them instead of matching a spelling kept here.
	Expect     Disposition `json:"expect"`
	ReplayOf   string      `json:"replay_of,omitempty"`
	ServerWant string      `json:"server_expect,omitempty"`
}

// Response is the service's answer as the fixture records it.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    map[string]any    `json:"body"`
}

// Find locates the fixture directory, returning it and every path
// tried. An empty directory with a non-empty trail means the fixtures
// are not present here, which is not an error.
func Find() (dir string, tried []string) {
	if env := os.Getenv(DirEnv); env != "" {
		tried = append(tried, env)
		if isDir(filepath.Join(env, "batches")) {
			return env, tried
		}
		return "", tried
	}
	cur, err := os.Getwd()
	if err != nil {
		return "", tried
	}
	for range 12 {
		// One spelling: the directory the fixtures live in, reached by
		// walking up until it appears beside us. Anything else — another
		// name, another layout — is what DirEnv is for.
		cand := filepath.Join(cur, "shared", "contract", "conformance")
		tried = append(tried, cand)
		if isDir(filepath.Join(cand, "batches")) {
			return cand, tried
		}
		up := filepath.Dir(cur)
		if up == cur {
			break
		}
		cur = up
	}
	return "", tried
}

// Load reads every case under dir, sorted by name so test output is
// stable.
func Load(dir string) ([]Case, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "batches"))
	if err != nil {
		return nil, err
	}
	var cases []Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		base := filepath.Join(dir, "batches", e.Name())
		if _, err := os.Stat(filepath.Join(base, "case.json")); err != nil {
			continue
		}
		c := Case{Name: e.Name()}
		if err := readJSON(filepath.Join(base, "case.json"), &c.Meta); err != nil {
			return nil, err
		}
		if err := readJSON(filepath.Join(base, "response.json"), &c.Response); err != nil {
			return nil, err
		}
		if err := readJSON(filepath.Join(base, "batch.json"), &c.Envelope); err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	return cases, nil
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
