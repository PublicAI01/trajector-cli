package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/platform"
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
func (r upstreamResolution) keepsRecordedUpstream(st ProjectStatus) bool {
	return !r.external && st.Enabled && st.Injected() && st.Upstream != "" && !st.UpstreamMoved.Happened()
}

// desiredUpstream resolves a project's upstream: an unsupported channel
// (Bedrock/Vertex) wins, then the user's own base-URL configuration,
// then the official endpoint. enable, doctor, and the session hook all
// answer from here; the surfaces differ only in presentation.
func (m *Machine) desiredUpstream(root string) upstreamResolution {
	if key, found := claudesettings.UnsupportedChannel(root, m.deps.Home, m.deps.Getenv); found {
		return upstreamResolution{unsupportedKey: key}
	}
	switch external, source, resolution := claudesettings.ExternalBaseURL(root, m.deps.Home, m.deps.Getenv); resolution {
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
func (m *Machine) reconcileUpstream(st ProjectStatus) (want upstreamResolution, moved, refused bool, err error) {
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
