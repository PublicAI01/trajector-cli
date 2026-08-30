package lifecycle

import (
	"context"
	"io"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
)

// assembly is what a served proxy of this device is built from. The
// machine holds the stores a command needs; this is the subset a
// serving process needs, handed over as values so the serve path cannot
// reach back into anything else the machine happens to hold.
func (m *Machine) assembly() proxyserve.Assembly {
	return proxyserve.Assembly{
		Layout:   m.deps.Layout,
		Tokens:   m.tokens,
		Service:  m.service,
		Consent:  m.consent,
		Version:  m.deps.Version,
		ExecPath: m.deps.ExecPath,
		Addr:     m.deps.ProxyAddr,
	}
}

// SuperviseProxy runs the watchdog process: it keeps a proxy child
// alive and ends with the child's clean idle exit.
func (m *Machine) SuperviseProxy(ctx context.Context, idle time.Duration, stdout, stderr io.Writer) error {
	return proxyserve.Supervise(ctx, m.assembly(), idle, stdout, stderr)
}

// ServeProxy serves the capture proxy in this process. The endpoint
// warning goes out here rather than inside the assembly: it is the same
// sentence enable prints, and both surfaces that commit data to an
// endpoint say it on their way in.
func (m *Machine) ServeProxy(ctx context.Context, idle time.Duration, stdout, stderr io.Writer) error {
	m.warnNonDefaultEndpoint(stderr)
	return proxyserve.Serve(ctx, m.assembly(), idle, stdout, stderr)
}
