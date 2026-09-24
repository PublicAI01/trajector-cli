package lifecycle

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// nonLoopbackUpstreamRemedy is the one explanation every surface
// prints for a refused upstream move.
const nonLoopbackUpstreamRemedy = "a non-loopback upstream must use https"

// upstreamResolution is the one answer to "where should this project's
// traffic go". Either the channel is unsupported — unsupportedKey names
// the setting, and nothing may be routed or rewritten — or upstream
// carries the destination.
type upstreamResolution struct {
	unsupportedKey string
	upstream       string
	source         claudesettings.Source
	external       bool
	// masked marks an upstream that could not be resolved because our
	// own injection stood where the user's shell configuration would
	// be. upstream then holds the official endpoint as the best guess
	// available to a surface that has to name one, but it is a guess:
	// nothing unattended may act on it.
	masked bool
}

// keepsRecordedUpstream reports that this project's grant is the only
// surviving record of where its traffic goes, so nothing may displace it
// on a guess. The chain names no base URL of the user's own — but
// injection writes into the very key, in the very file, the chain reads
// first, so that silence can be ours rather than theirs.
//
// What tells the two apart is which side last wrote the grant. An
// upstream enable recorded was resolved while the user was watching, and
// enable is also what overwrote the chain's copy of it, so the silence
// is ours and the grant is all that is left. An upstream a later
// unattended move recorded came from the chain itself, so the chain
// falling silent is that same source speaking, and following it back is
// right — which is why the term is UpstreamMoved and not merely "a grant
// exists".
//
// masked covers only the shell spelling of this. The settings-file
// spelling had no guard outside enable until 2026-08-21: a relay kept in
// .claude/settings.local.json read as "nothing configured" to the
// session hook and to doctor, so reconcileUpstream moved the grant to
// the official endpoint and every later request carried the relay's
// credentials to Anthropic. Enable and the unattended reconcile answer
// from this one spelling so they cannot drift apart again.
func (r upstreamResolution) keepsRecordedUpstream(st report.ProjectStatus) bool {
	return !r.external && st.Enabled && st.Injected && st.Upstream != "" && !st.UpstreamMoved.Happened()
}

// ErrInjectionWithoutGrant reports an enable that cannot see where this
// project's traffic goes, because trajector's own base URL stands in the
// project's settings file and no grant records what it wrote over. It
// is the settings-file sibling of ErrUpstreamMasked: guessing the
// official endpoint would send a relay user's credentials somewhere they
// never chose, so enable stops and says how to put the value back.
var ErrInjectionWithoutGrant = errors.New(
	"trajector's own base URL stands in this project's settings, but the routing table records no grant for this project, " +
		"so the base URL it wrote over is unknown")

// injectionOutlivedGrant reports trajector's own base URL standing in
// the project's settings file while no entry of the routing table —
// standing or revoked — names this project or the token the injection
// carries. keepsRecordedUpstream answers from the grant; here there is
// no grant to answer from, and the chain is silent for the reason
// described above: the injection wrote over the one key the user's own
// value lived in. The official endpoint is then a guess like any other.
//
// This is how a routing table that went missing reaches enable and
// doctor: the user moves an unreadable table aside, every injection
// stays, and the grant that held each relay is no longer read. Until
// 2026-09-24 enable granted the official endpoint there without a word,
// and doctor removed the injection as stale with nothing to put back,
// so a relay kept in .claude/settings.local.json was lost either way.
// A revoked entry is still a record: doctor removes such an injection
// and restores from it. The unattended reconcile runs only over a
// standing grant, so it never meets this state; enable and doctor ask
// it through this one predicate.
func (m *Machine) injectionOutlivedGrant(st report.ProjectStatus) (bool, error) {
	if st.Enabled || st.InjectedBaseURL == "" {
		return false, nil
	}
	grants, err := m.routes.All()
	if err != nil {
		return false, err
	}
	return !slices.ContainsFunc(grants, func(g routing.Grant) bool {
		return g.RootPath == st.Root || (st.InjectedToken != "" && g.Token == st.InjectedToken)
	}), nil
}

// injectionWithoutGrantSteps are the steps that end the state, as
// enable and doctor both state them.
func injectionWithoutGrantSteps() []string {
	return append(report.BaseURLRestoreSteps(), "Then run `trajector enable` in this project.")
}

// injectionWithoutGrantRemedy is ErrInjectionWithoutGrant with those
// steps, as enable reports it.
func injectionWithoutGrantRemedy() error {
	return fmt.Errorf("%w. %s", ErrInjectionWithoutGrant, strings.Join(injectionWithoutGrantSteps(), " "))
}

// desiredUpstream resolves a project's upstream: an unsupported channel
// (Bedrock/Vertex) wins, then the user's own base-URL configuration,
// then the official endpoint. enable, doctor, and the session hook all
// answer from here; the surfaces differ only in presentation.
func (m *Machine) desiredUpstream(root string) upstreamResolution {
	if key, found := claudesettings.UnsupportedChannel(root, m.claude(), m.deps.Getenv); found {
		return upstreamResolution{unsupportedKey: key}
	}
	switch external, source, resolution := claudesettings.ExternalBaseURL(root, m.claude(), m.deps.Getenv); resolution {
	case claudesettings.BaseURLExternal:
		return upstreamResolution{upstream: external, source: source, external: true}
	case claudesettings.BaseURLMasked:
		return upstreamResolution{upstream: capture.Anthropic.OfficialUpstream, masked: true}
	}
	return upstreamResolution{upstream: capture.Anthropic.OfficialUpstream}
}

// reconcileUpstream moves the project's recorded upstream to the
// resolved one when they differ, and leaves it alone whenever the
// resolution is a guess — see keepsRecordedUpstream for the case where
// our own injection is what made the chain go quiet. An unsupported
// channel moves nothing:
// the grant keeps the upstream it was enabled with, which is also what
// doctor reports — the hook and doctor must give one answer. An
// upstream we could not resolve moves nothing either: the session hook
// runs as a child of the very process that applied our injection, so
// the shell's own base URL is invisible to it, and a guess there would
// redirect a relay user's credentialed traffic to the official
// endpoint on the first session after enable. What the grant records
// was resolved with the user watching; leave it. A move to a plaintext
// non-loopback destination is refused (reported in refused): this path
// runs without a user watching, and settings a repository ships reach
// it, so it must not silently point credentialed traffic somewhere
// that would carry it unencrypted. enable is untouched — there the
// user sees the third-party notice and decides.
func (m *Machine) reconcileUpstream(st report.ProjectStatus) (want upstreamResolution, moved, refused bool, err error) {
	want = m.desiredUpstream(st.Root)
	if want.unsupportedKey != "" || want.masked || want.keepsRecordedUpstream(st) || want.upstream == st.Upstream {
		return want, false, false, nil
	}
	if !platform.CredentialSafeURL(want.upstream) {
		return want, false, true, nil
	}
	if err := m.routes.SetUpstream(st.Root, want.upstream, m.now()); err != nil {
		return want, false, false, err
	}
	return want, true, false, nil
}
