// Package lifecycle is the consent lifecycle of one device and its
// projects: pairing, signing out, enabling and disabling a project,
// uninstalling, and keeping the capture proxy up. It is the composition
// root — the directories, the token store, the service client, and this
// build's identity are assembled once here — so no command has to know
// how any of them are put together.
package lifecycle

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Deps is everything the machine needs from the world outside it.
type Deps struct {
	Layout   userdirs.Layout
	Tokens   *tokenstore.Store
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

// askYesNo puts one yes/no question to the user. Only an explicit yes
// answers true; a read that yields nothing at all is the error.
func askYesNo(io IO, prompt string) (bool, error) {
	fmt.Fprint(io.Out, prompt)
	line, err := bufio.NewReader(io.In).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "yes", "y":
		return true, nil
	}
	return false, nil
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

// warnNonDefaultEndpoint prints one line when this machine is
// configured to send data somewhere other than the default trajector
// service, so an endpoint override is never silent. Both surfaces that
// commit data to the endpoint — enable and the serving proxy — print
// it on their way in.
func (m *Machine) warnNonDefaultEndpoint(w io.Writer) {
	if url := m.deps.Platform.BaseURL(); url != platform.DefaultBaseURL {
		fmt.Fprintf(w, "WARNING: uploads go to %s, not the default trajector service.\n", url)
	}
}

// Paired reports whether this device holds a pairing token.
func (m *Machine) Paired() bool {
	_, ok := m.deviceToken()
	return ok
}

func (m *Machine) deviceToken() (string, bool) {
	token, ok, err := m.deps.Tokens.DeviceToken()
	if err != nil {
		return "", false
	}
	return token, ok
}

func (m *Machine) now() string { return m.deps.Now().UTC().Format(time.RFC3339) }
