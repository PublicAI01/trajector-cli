package report

import (
	"errors"

	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

// The one instruction every surface prints with each port-holder
// verdict, so the advice cannot drift between surfaces. Only ProxyRemedy
// maps a verdict to its instruction.
const (
	portOccupiedRemedy    = "Enabled projects route API credentials at this address; find and stop the process holding the port, or run `trajector disable` in enabled projects."
	proxyUnverifiedRemedy = "This is usually an authentication problem (the proxy's published admin token is missing or stale), not a foreign process. The proxy publishes a fresh token each time it starts and exits on its own once idle, so a later session usually clears it; there is no process to stop."
)

// ProxyRemedy is the follow-up instruction a surface prints under a
// failed port-holder verdict, empty when the verdict's own words are
// the whole story. Advising the user to stop the port's holder is
// reserved for a proven stranger: an unverified holder may be their own
// proxy.
func ProxyRemedy(why error) string {
	switch {
	case errors.Is(why, proxylife.ErrPortOccupied):
		return portOccupiedRemedy
	case errors.Is(why, proxylife.ErrProxyUnverified):
		return proxyUnverifiedRemedy
	}
	return ""
}
