package proxytest

import (
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
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
type Grant struct {
	Token         string
	ProjectIDHash string
	RootPath      string
	Upstream      string
	GrantedAt     string
}

// GrantProject records a project as enabled, as `trajector enable`
// would.
func (s *Sandbox) GrantProject(g Grant) {
	s.t.Helper()
	err := routing.OpenStore(s.layout.RoutingTable()).Grant(routing.Grant{
		Token:         g.Token,
		ProjectIDHash: g.ProjectIDHash,
		RootPath:      g.RootPath,
		Upstream:      g.Upstream,
		GrantedAt:     "2026-08-01T00:00:00Z",
	})
	if err != nil {
		s.t.Fatal(err)
	}
}

// ActiveGrant reports the live grant for a project root, if any.
func (s *Sandbox) ActiveGrant(root string) (Grant, bool) {
	s.t.Helper()
	g, ok, err := routing.OpenStore(s.layout.RoutingTable()).Active(root)
	if err != nil {
		s.t.Fatal(err)
	}
	if !ok {
		return Grant{}, false
	}
	return Grant{
		Token:         g.Token,
		ProjectIDHash: g.ProjectIDHash,
		RootPath:      g.RootPath,
		Upstream:      g.Upstream,
		GrantedAt:     g.GrantedAt,
	}, true
}

// PausedReason reports why recording is suspended device-wide, or empty
// when it is not.
func (s *Sandbox) PausedReason() string {
	s.t.Helper()
	reason, err := routing.OpenStore(s.layout.RoutingTable()).PausedReason()
	if err != nil {
		s.t.Fatal(err)
	}
	return reason
}

// Pause suspends recording device-wide, as signing out would.
func (s *Sandbox) Pause(reason string) {
	s.t.Helper()
	if err := routing.OpenStore(s.layout.RoutingTable()).Pause(reason); err != nil {
		s.t.Fatal(err)
	}
}

// Recording reports what the proxy would decide for a token, read the
// same way the proxy reads it.
func (s *Sandbox) Recording(token string) (known, recording bool) {
	s.t.Helper()
	_, verdict := routing.New(s.layout.RoutingTable(), time.Nanosecond).Lookup(token)
	return verdict.Resolves(), verdict.Records()
}

// SeedRawcall stores one rawcall for a project, as a capture would.
func (s *Sandbox) SeedRawcall(id, projectIDHash string, at time.Time) {
	s.t.Helper()
	sp, err := spool.Create(s.layout.SpoolDir(), 0)
	if err != nil {
		s.t.Fatal(err)
	}
	env, err := envelope.Record(envelope.Observation{
		Provider: "anthropic", Endpoint: "/v1/messages", HTTPStatus: 200,
		ProjectIDHash: projectIDHash, At: at,
		Request:     []byte(`{"model":"claude-fable-5"}`),
		Response:    []byte(`{"id":"` + id + `"}`),
		ContentType: "application/json", RequestComplete: true, ResponseComplete: true,
	})
	if err != nil {
		s.t.Fatal(err)
	}
	entry := spool.Entry{RequestID: env.RequestID(), SessionKey: env.SessionKey(), Timestamp: at}
	if err := sp.Write(entry, env.Bytes()); err != nil {
		s.t.Fatal(err)
	}
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
