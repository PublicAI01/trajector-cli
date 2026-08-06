package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
)

// upstreamResolution is the one answer to "where should this project's
// traffic go". Either the channel is unsupported — unsupportedKey names
// the setting, and nothing may be routed or rewritten — or upstream
// carries the destination.
type upstreamResolution struct {
	unsupportedKey string
	upstream       string
	source         claudesettings.Source
	external       bool
}

// desiredUpstream resolves a project's upstream: an unsupported channel
// (Bedrock/Vertex) wins, then the user's own base-URL configuration,
// then the official endpoint. enable, doctor, and the session hook all
// answer from here; the surfaces differ only in presentation.
func (m *Machine) desiredUpstream(root string) upstreamResolution {
	if key, found := claudesettings.UnsupportedChannel(root, m.deps.Home, m.deps.Getenv); found {
		return upstreamResolution{unsupportedKey: key}
	}
	if external, source, found := claudesettings.ExternalBaseURL(root, m.deps.Home, m.deps.Getenv); found {
		return upstreamResolution{upstream: external, source: source, external: true}
	}
	return upstreamResolution{upstream: capture.Anthropic.OfficialUpstream}
}

// reconcileUpstream moves the project's recorded upstream to the
// resolved one when they differ. An unsupported channel moves nothing:
// the grant keeps the upstream it was enabled with, which is also what
// doctor reports — the hook and doctor must give one answer.
func (m *Machine) reconcileUpstream(root, current string) (want upstreamResolution, moved bool, err error) {
	want = m.desiredUpstream(root)
	if want.unsupportedKey != "" || want.upstream == current {
		return want, false, nil
	}
	if err := m.routes.SetUpstream(root, want.upstream); err != nil {
		return want, false, err
	}
	return want, true, nil
}
