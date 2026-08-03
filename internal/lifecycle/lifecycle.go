// Package lifecycle is the consent lifecycle of one device and its
// projects: pairing, signing out, enabling and disabling a project,
// uninstalling, and keeping the capture proxy up. It is the composition
// root — the directories, the token store, the service client, and this
// build's identity are assembled once here — so no command has to know
// how any of them are put together.
package lifecycle

import (
	"fmt"
	"io"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Pause reasons written into the routing table. Each writer resumes
// only its own reason, so accepting a new agreement cannot silently
// lift a signed-out pause. They are not exported: the machine is the
// only thing that both sets and clears them.
const (
	pauseSignedOut = "signed_out"
	pauseConsent   = "consent_reconfirm"
)

// Deps is everything the machine needs from the world outside it.
type Deps struct {
	Layout   userdirs.Layout
	Tokens   tokenstore.Store
	Platform *platform.Client
	Version  string
	// ExecPath is this binary, injected into session hooks and used to
	// spawn the proxy.
	ExecPath  string
	ProxyAddr string
	// Home is the user's home directory, where user-wide Claude Code
	// settings live.
	Home   string
	Getenv func(string) string
	Now    func() time.Time
}

// IO is where one operation talks to the user.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Machine drives the device and project consent lifecycle.
type Machine struct {
	deps    Deps
	proxy   *proxylife.Proxy
	routes  *routing.Store
	consent *consent.Store
}

// Open assembles the machine. It is the only place these collaborators
// are wired together.
func Open(deps Deps) (*Machine, error) {
	switch {
	case deps.Tokens == nil:
		return nil, fmt.Errorf("lifecycle: a token store is required")
	case deps.Platform == nil:
		return nil, fmt.Errorf("lifecycle: a service client is required")
	}
	if deps.Getenv == nil {
		deps.Getenv = func(string) string { return "" }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Machine{
		deps:    deps,
		proxy:   proxylife.For(deps.Layout, deps.Version, deps.ExecPath, deps.ProxyAddr),
		routes:  routing.OpenStore(deps.Layout.RoutingTable()),
		consent: consent.Open(deps.Layout.ConsentFile()),
	}, nil
}

// Paired reports whether this device holds a pairing token.
func (m *Machine) Paired() bool {
	_, ok := m.deviceToken()
	return ok
}

func (m *Machine) deviceToken() (string, bool) {
	secret, err := m.deps.Tokens.Load(tokenstore.DeviceTokenName)
	if err != nil || len(secret) == 0 {
		return "", false
	}
	return string(secret), true
}

func (m *Machine) now() string { return m.deps.Now().UTC().Format(time.RFC3339) }
