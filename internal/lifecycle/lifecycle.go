// Package lifecycle is the consent lifecycle of one device and its
// projects: pairing, signing out, enabling and disabling a project,
// uninstalling, and keeping the capture proxy up. It is the composition
// root — the directories, the token store, the service client, and this
// build's identity are assembled once here, from plain values — so no
// command has to know how any of them are put together, and no command
// can hand the machine a collaborator of its own.
//
// The one assembly it does not perform itself is the serving proxy's:
// that process wires a spool, a routing table, and a resident uploader
// no command has any use for, so proxyserve states what it is built
// from and this package hands it those values.
package lifecycle

import (
	"bufio"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/gitsnapshot"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Deps is everything the machine needs from the world outside it:
// where this device's files are, where its data goes, who this build
// is, and how it reads the clock and the environment. The stores and
// the service client are not among them — Open builds those, so no
// caller has to know how they are put together.
type Deps struct {
	Layout userdirs.Layout
	// PlatformURL is the trajector service this device pairs with and
	// uploads to; empty means the default one. The token store that
	// authenticates those requests is opened from Layout's secrets
	// directory.
	PlatformURL string
	Version     string
	// ExecPath is this binary, injected into session hooks, used to
	// spawn the proxy, and replaced in place by upgrade.
	ExecPath string
	// Releases is the index of published releases upgrade reads.
	Releases  string
	ProxyAddr string
	// Spawn starts every process this machine leaves running behind a
	// command: the proxy, and the reader a session hook hands its work
	// to. Nil detaches them, which is what production does. A test suite
	// that drives the CLI in its own process supplies a starter that
	// starts nothing, so neither process outlives it.
	Spawn proxylife.Starter
	// Home is the user's home directory. Claude Code's own directories
	// are resolved from it, and from the environment that moves them.
	Home   string
	Getenv func(string) string
	// Now is the record-keeping clock: it answers "what timestamp goes on
	// this record", which is why m.now() is the only thing that reads it.
	// It is pinned to a fixed instant by the test harness and by
	// TRAJECTOR_NOW, so nothing may ask it how much time has passed.
	Now func() time.Time
	// PairingWindow bounds how long Pair waits for browser approval, on
	// the wall clock. Zero selects defaultPairingWindow. It is stated as a
	// duration rather than measured against Now for exactly the reason
	// above.
	PairingWindow time.Duration
}

// IO is where one operation talks to the user.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// OutStyle is how much of a terminal Out can show. It is named for
	// the one stream it was detected from: what Err can show is that
	// stream's own answer, and nothing the machine writes there is
	// styled. Its zero value is plain ASCII, so a caller that hands the
	// machine a buffer — every test does — reads text with nothing in
	// it that a terminal would have to interpret.
	OutStyle report.Style
}

// askYesNo puts one yes/no question to the user. Empty input takes
// def; otherwise only an explicit yes answers true. The error is a
// read that yields nothing at all — a script, a pipeline, a closed
// stdin — which is the one condition under which no question of any
// kind can be answered.
func askYesNo(io IO, prompt string, def bool) (bool, error) {
	fmt.Fprint(io.Out, prompt)
	line, err := bufio.NewReader(io.In).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, nil
	case "yes", "y":
		return true, nil
	}
	return false, nil
}

// Machine drives the device and project consent lifecycle.
type Machine struct {
	deps    Deps
	tokens  *tokenstore.Store
	service *platform.Client
	proxy   *proxylife.Proxy
	routes  *routing.Store
	consent *consent.Store
	// registry holds which session files each enabled project reads and
	// how far each is read. It is a store of this device like the ones
	// above, so it is opened here and never anywhere else.
	registry *follow.Registry
	// heads holds the commit this device last observed for each enabled
	// project, which is what a session-scoped observation compares
	// against. It is local state of the same kind as a reading cursor.
	heads *gitsnapshot.Heads
}

// claude is where Claude Code keeps its files for this process. It is
// resolved on every call because a hook runs in the session's
// environment, which may move those directories, and not in the one
// this machine was opened in.
func (m *Machine) claude() claudesettings.Host {
	return claudesettings.HostFor(runtime.GOOS, m.deps.Home, m.deps.Getenv)
}

// Open assembles the machine. It is the only place these collaborators
// are wired together, so a caller cannot hand the machine a store or a
// client of its own — and nothing it assembles can fail, which is why
// there is nothing to report back.
func Open(deps Deps) *Machine {
	if deps.PlatformURL == "" {
		deps.PlatformURL = platform.DefaultBaseURL
	}
	if deps.Getenv == nil {
		deps.Getenv = func(string) string { return "" }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	deps.Spawn = deps.Spawn.OrDetached()
	return &Machine{
		deps:    deps,
		tokens:  tokenstore.Open(deps.Layout.SecretsDir()),
		service: platform.New(deps.PlatformURL, deps.Version),
		proxy:   proxylife.For(deps.Layout, deps.Version, deps.ExecPath, deps.ProxyAddr, deps.Spawn),
		routes:  routing.OpenStore(deps.Layout.RoutingTable()),
		consent: consent.Open(deps.Layout.ConsentFile()),

		registry: follow.Open(deps.Layout.FollowDir()),
		heads:    gitsnapshot.OpenHeads(deps.Layout.GitHeadsFile()),
	}
}

// warnNonDefaultEndpoint prints one line when this machine is
// configured to send data somewhere other than the default trajector
// service, so an endpoint override is never silent. Both surfaces that
// commit data to the endpoint — enable and the serving proxy — print
// it on their way in.
func (m *Machine) warnNonDefaultEndpoint(w io.Writer) {
	if url := m.service.BaseURL(); url != platform.DefaultBaseURL {
		fmt.Fprintf(w, "WARNING: uploads go to %s, not the default trajector service.\n", url)
	}
}

// Paired reports whether this device holds a pairing token.
func (m *Machine) Paired() bool {
	_, ok := m.deviceToken()
	return ok
}

func (m *Machine) deviceToken() (string, bool) {
	token, ok, err := m.tokens.DeviceToken()
	if err != nil {
		return "", false
	}
	return token, ok
}

func (m *Machine) now() string { return m.deps.Now().UTC().Format(time.RFC3339) }
