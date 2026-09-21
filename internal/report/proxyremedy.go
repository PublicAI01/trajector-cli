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
	portHeldRemedy        = "Enabled projects route API credentials at this address; free the port by stopping whatever holds it, or run `trajector disable` in enabled projects."
	proxyUnverifiedRemedy = "This is usually an authentication problem (the proxy's published admin token is missing or stale), not a foreign process. The proxy publishes a fresh token each time it starts and exits on its own once idle, so a later session usually clears it; there is no process to stop."

	// portHeldNextStep is what ends a held port, in the order the two
	// steps run. Freeing the port is the user's to do — no command of
	// trajector's takes a port from another process — so it is stated
	// here and not on a fix line, which carries a command and nothing
	// else.
	portHeldNextStep = "Once the port is free, run `trajector doctor`."
)

// HolderProcess is what a run could read about the process holding the
// proxy port: its process id and what the operating system calls it,
// as the operating system reports them. It is the zero value wherever
// nothing could be read, which is the usual answer for a process of
// another user and on a platform this build has no reading for. No
// surface ever says what the holder is for: naming a program from a
// process name would be a guess, and the user is the one who can look.
type HolderProcess struct {
	PID  int
	Name string
}

// Read reports whether anything was read about the holder.
func (h HolderProcess) Read() bool { return h.PID > 0 }

// String is the holder as a surface states it.
func (h HolderProcess) String() string {
	if h.Name == "" {
		return fmt.Sprintf("pid %d", h.PID)
	}
	return fmt.Sprintf("%s (pid %d)", h.Name, h.PID)
}

// proxyWhy is the one sentence that says what a failed port-holder
// verdict costs the user. The verdict's own words are the headline; this
// is what follows from them, and it is stated apart from the remedy so
// that the line the user copies holds a command and nothing else.
func proxyWhy(why error) string {
	if proxylife.PortIsHeld(why) {
		return "another process holds the proxy port, so enabled projects cannot route through it"
	}
	return "this device could not confirm the proxy holding the port is its own"
}

// ProxyRemedy is the follow-up instruction a surface prints under a
// failed port-holder verdict, empty when the verdict's own words are
// the whole story. Advising the user to stop the port's holder is
// reserved for a holder this build proved is not its own: an
// unverified holder may be their own proxy.
func ProxyRemedy(why error) string {
	switch {
	case proxylife.PortIsHeld(why):
		return portHeldRemedy
	case errors.Is(why, proxylife.ErrProxyUnverified):
		return proxyUnverifiedRemedy
	}
	return ""
}

// findHolderCommand introduces the command that names the process
// holding addr. The sentence is this package's; which command answers
// it is the platform's, and the package that runs one knows that. It
// is printed rather than run: the user asks their own machine who
// holds the port, and this build never says what the answer will be.
func findHolderCommand(addr string) string {
	return "To find the holder: " + proxylife.HolderCommand(addr)
}

// proxyProblem is the one line every surface states for a failed
// port-holder verdict: the verdict's own words, what they cost, and
// the instruction that follows where there is one. The instruction is
// carried by the problem rather than written beside it, so no surface
// can order it away from what it explains.
//
// A held port names no fix command: nothing trajector runs takes a
// port from another process, and a fix line that named one would send
// the user to a command that cannot succeed. What ends it is stated
// under the remedy, in the order the steps run. holder is what the
// surface could read about the process at the port, and is stated as
// an observation — the zero value states nothing.
func proxyProblem(why error, holder HolderProcess) line {
	one := line{
		severity: severityError,
		text:     trimPeriod(fmt.Sprintf("%v", why)),
		why:      proxyWhy(why),
		fix:      fixDoctor,
	}
	if remedy := ProxyRemedy(why); remedy != "" {
		one.details = []string{remedy}
	}
	if !proxylife.PortIsHeld(why) {
		return one
	}
	one.fix = ""
	if addr, ok := proxylife.HeldPort(why); ok {
		one.details = append(one.details, findHolderCommand(addr))
	}
	if holder.Read() {
		one.details = append(one.details, "At the port as this device reads it: "+holder.String()+".")
	}
	one.details = append(one.details, portHeldNextStep)
	return one
}

// ProxyProblem writes that same line for a command that has nothing
// else to say about the run: what is left to its caller is the exit
// code. Such a command looked at no process, so it states none.
func ProxyProblem(w io.Writer, style Style, why error) {
	render(w, style, []line{proxyProblem(why, HolderProcess{})}, layout{})
}

// ProxyProblem records it among a doctor run's findings, with what the
// run could read about the holder.
func (f *Findings) ProxyProblem(why error, holder HolderProcess) {
	f.take(proxyProblem(why, holder))
}
