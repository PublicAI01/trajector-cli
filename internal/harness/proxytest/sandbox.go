package proxytest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Sandbox reads and seeds the files a proxy shares with the CLI: the
// routing table and the spool. Tests use it to set up preconditions and
// to check what a command left behind, without naming the file formats
// themselves.
type Sandbox struct {
	t      *testing.T
	layout userdirs.Layout
}

// Open wraps the trajector files under layout.
func Open(t *testing.T, layout userdirs.Layout) *Sandbox {
	return &Sandbox{t: t, layout: layout}
}

// Sandbox reads this proxy's own files.
func (e *Env) Sandbox() *Sandbox { return Open(e.t, e.layout) }

// Grant is what the routing table records for one enabled project.
// The harness reads and writes the table through routing's own type:
// it does not redeclare the contract.
type Grant = routing.Grant

// Shape is the form a grant records, in routing's own type, with the
// two shapes a project can be enabled in. Tests name them through the
// harness so nothing above the proxy has to reach into the routing
// table's vocabulary.
type Shape = routing.Shape

const (
	WithProxy    = routing.WithProxy
	WithoutProxy = routing.WithoutProxy
)

// seedTime is the instant every seeder stamps when the test does not
// care when something happened.
var seedTime = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

// GrantProject records a project as enabled, as `trajector enable`
// would. An empty GrantedAt gets a fixed timestamp: most tests do not
// care when.
func (s *Sandbox) GrantProject(g Grant) {
	s.t.Helper()
	if g.GrantedAt == "" {
		g.GrantedAt = seedTime.Format(time.RFC3339)
	}
	if err := routing.OpenStore(s.layout.RoutingTable()).Grant(g); err != nil {
		s.t.Fatal(err)
	}
}

// RevokeProject withdraws a project's grant behind the back of whatever
// else records it, as a half-finished withdrawal would have left the
// table.
func (s *Sandbox) RevokeProject(root, at string) {
	s.t.Helper()
	if at == "" {
		at = seedTime.Format(time.RFC3339)
	}
	if err := routing.OpenStore(s.layout.RoutingTable()).Revoke(root, at); err != nil {
		s.t.Fatal(err)
	}
}

// ActiveGrant reports the live grant for a project root, if any.
func (s *Sandbox) ActiveGrant(root string) (Grant, bool) {
	s.t.Helper()
	// A grant is stored under the consent root, with symbolic links
	// resolved; a test names the directory it made, which on macOS and
	// Windows is not that spelling.
	g, ok, err := routing.OpenStore(s.layout.RoutingTable()).Active(CanonicalRoot(s.t, root))
	if err != nil {
		s.t.Fatal(err)
	}
	return g, ok
}

// PauseReason is why recording is suspended device-wide, in routing's
// own type, with the reasons the device can be left in. Tests name them
// through the harness so nothing above the proxy has to reach into the
// routing table's vocabulary.
type PauseReason = routing.PauseReason

const (
	PauseSignedOut         = routing.PauseSignedOut
	PauseConsentReconfirm  = routing.PauseConsentReconfirm
	PauseConsentUnreadable = routing.PauseConsentUnreadable
	PauseRedactionDrift    = routing.PauseRedactionDrift
)

// PausedReason reports why recording is suspended device-wide, or empty
// when it is not.
func (s *Sandbox) PausedReason() routing.PauseReason {
	s.t.Helper()
	reason, err := routing.OpenStore(s.layout.RoutingTable()).PausedReason()
	if err != nil {
		s.t.Fatal(err)
	}
	return reason
}

// Pause suspends recording device-wide, as signing out would.
func (s *Sandbox) Pause(reason routing.PauseReason) {
	s.t.Helper()
	if err := routing.OpenStore(s.layout.RoutingTable()).Pause(reason); err != nil {
		s.t.Fatal(err)
	}
}

// PauseByBuild suspends recording device-wide and records which build
// did it, as a build that cannot mask what it read would.
func (s *Sandbox) PauseByBuild(reason routing.PauseReason, version string) {
	s.t.Helper()
	if err := routing.OpenStore(s.layout.RoutingTable()).PauseByBuild(reason, version); err != nil {
		s.t.Fatal(err)
	}
}

// ResumeOtherBuild lifts a standing pause of the given reason when the
// named build is not the one that set it, as the doctor of that build
// would, and reports whether it lifted anything. A build that lifts
// nothing is the build the pause is attributed to.
func (s *Sandbox) ResumeOtherBuild(reason routing.PauseReason, version string) bool {
	s.t.Helper()
	resumed, _, err := routing.OpenStore(s.layout.RoutingTable()).ResumeOtherBuild(reason, version)
	if err != nil {
		s.t.Fatal(err)
	}
	return resumed
}

// RoutingTablePath reports where the routing table lives, for tests
// that assert a surface names the file it could not read.
func (s *Sandbox) RoutingTablePath() string { return s.layout.RoutingTable() }

// CorruptRoutingTable leaves the routing table unparseable: the bytes
// on disk are a JSON document that stops mid-key, as a crash partway
// through a write by anything but the table's own atomic writer leaves
// it.
func (s *Sandbox) CorruptRoutingTable() {
	s.t.Helper()
	writeFile(s.t, s.layout.RoutingTable(), `{"projects":{"tok`)
}

// BlockRoutingTable puts a directory where the routing table is: the
// path exists and no read of it succeeds. It stands for a table the
// user cannot read, and holds for every user on every platform, where
// a file mode does not.
func (s *Sandbox) BlockRoutingTable() {
	s.t.Helper()
	path := s.layout.RoutingTable()
	if err := os.RemoveAll(path); err != nil {
		s.t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		s.t.Fatal(err)
	}
}

// MoveRoutingTableAside renames the routing table to a sibling path, as
// a user does with a table that cannot be read, and reports where the
// file went. Nothing is left at the table's own path.
func (s *Sandbox) MoveRoutingTableAside() string {
	s.t.Helper()
	path := s.layout.RoutingTable()
	aside := path + ".aside"
	if err := os.Rename(path, aside); err != nil {
		s.t.Fatal(err)
	}
	return aside
}

// Recording reports what the proxy would decide for a token, read the
// same way the proxy reads it.
func (s *Sandbox) Recording(token string) (known, recording bool) {
	s.t.Helper()
	_, verdict := routing.New(s.layout.RoutingTable(), time.Nanosecond).Lookup(token)
	return verdict.Resolves(), verdict.Records()
}

// Observation is what was seen of one exchange, in envelope's own type.
type Observation = envelope.Observation

// RawcallOption refines the observation a seeded rawcall records
// beyond the minimal valid default.
type RawcallOption func(*Observation)

// SeedRawcall stores one rawcall for a project, as a capture would.
func (s *Sandbox) SeedRawcall(id, projectIDHash string, at time.Time, opts ...RawcallOption) {
	s.t.Helper()
	sp, err := spool.Create(s.layout.SpoolDir(), 0)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := sp.Write(record(s.t, id, projectIDHash, at, opts)); err != nil {
		s.t.Fatal(err)
	}
}

// SeedTornRawcall stores one rawcall and then truncates its file to the
// front half, leaving what a write torn by a crash leaves: a file named
// like a record that no longer reads back as one.
func (s *Sandbox) SeedTornRawcall(id, projectIDHash string, at time.Time) {
	s.t.Helper()
	s.SeedRawcall(id, projectIDHash, at)
	matches, err := filepath.Glob(filepath.Join(s.layout.SpoolDir(), "*", id+".json"))
	if err != nil || len(matches) != 1 {
		s.t.Fatalf("locating the stored rawcall %s: %v (matches: %v)", id, err, matches)
	}
	data, err := fsatomic.ReadFile(matches[0])
	if err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(matches[0], data[:len(data)/2], 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// Rawcall builds one rawcall's envelope bytes, exactly what SeedRawcall
// stores, for tests that place records somewhere other than the spool.
func Rawcall(t *testing.T, id, projectIDHash string, at time.Time, opts ...RawcallOption) []byte {
	t.Helper()
	return record(t, id, projectIDHash, at, opts).Bytes()
}

// record is the one place tests get a valid rawcall from.
func record(t *testing.T, id, projectIDHash string, at time.Time, opts []RawcallOption) envelope.Envelope {
	t.Helper()
	obs := envelope.Observation{
		Provider: "anthropic", Endpoint: "/v1/messages", HTTPStatus: 200,
		ProjectIDHash: projectIDHash, At: at,
		Request:     []byte(`{"model":"claude-fable-5"}`),
		Response:    []byte(`{"id":"` + id + `"}`),
		ContentType: "application/json", RequestComplete: true, ResponseComplete: true,
	}
	for _, apply := range opts {
		apply(&obs)
	}
	env, err := envelope.Record(obs)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// Rawcalls reports every rawcall currently stored.
func (s *Sandbox) Rawcalls() []spool.Rawcall {
	s.t.Helper()
	sp, err := spool.Open(s.layout.SpoolDir(), 0)
	if err != nil {
		s.t.Fatal(err)
	}
	var stored []spool.Rawcall
	if err := sp.Each(func(r spool.Rawcall) error {
		stored = append(stored, r)
		return nil
	}); err != nil {
		s.t.Fatal(err)
	}
	return stored
}

// ProjectsWithRawcalls reports which projects still have stored data.
func (s *Sandbox) ProjectsWithRawcalls() map[string]int {
	s.t.Helper()
	counts := map[string]int{}
	for _, r := range s.Rawcalls() {
		hash, ok := envelope.ProjectIDHashOf(r.Data)
		if !ok {
			continue
		}
		counts[hash]++
	}
	return counts
}
