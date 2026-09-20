package report

import (
	"errors"
	"fmt"
	"io"

	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

// The one instruction every surface prints with each port-holder
// verdict, so the advice cannot drift between surfaces. Only ProxyRemedy
// maps a verdict to its instruction.
const (
	portOccupiedRemedy    = "Enabled projects route API credentials at this address; find and stop the process holding the port, or run `trajector disable` in enabled projects."
	proxyUnverifiedRemedy = "This is usually an authentication problem (the proxy's published admin token is missing or stale), not a foreign process. The proxy publishes a fresh token each time it starts and exits on its own once idle, so a later session usually clears it; there is no process to stop."
)

// proxyWhy is the one sentence that says what a failed port-holder
// verdict costs the user. The verdict's own words are the headline; this
// is what follows from them, and it is stated apart from the remedy so
// that the line the user copies holds a command and nothing else.
func proxyWhy(why error) string {
	if errors.Is(why, proxylife.ErrPortOccupied) {
		return "another process holds the proxy port, so enabled projects cannot route through it"
	}
	return "this device could not confirm the proxy holding the port is its own"
}

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

// proxyProblem is the one line every surface states for a failed
// port-holder verdict: the verdict's own words, what they cost, the
// command that looks into the holder, and the instruction that follows
// where there is one. The instruction is carried by the problem rather
// than written beside it, so no surface can order it away from what it
// explains.
func proxyProblem(why error) line {
	one := line{
		severity: severityError,
		text:     trimPeriod(fmt.Sprintf("%v", why)),
		why:      proxyWhy(why),
		fix:      fixDoctor,
	}
	if remedy := ProxyRemedy(why); remedy != "" {
		one.details = []string{remedy}
	}
	return one
}

// ProxyProblem writes that same line for a command that has nothing
// else to say about the run: what is left to its caller is the exit
// code.
func ProxyProblem(w io.Writer, style Style, why error) {
	render(w, style, []line{proxyProblem(why)}, layout{})
}

// ProxyProblem records it among a doctor run's findings.
func (f *Findings) ProxyProblem(why error) { f.take(proxyProblem(why)) }
