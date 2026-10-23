package lifecycle

import (
	"context"

	"github.com/PublicAI01/trajector-cli/internal/report"
)

// Status prints the device dashboard and returns how many of its lines
// said something stopped working. It resolves the diagnosis and hands
// it to the renderer: status repairs nothing, always leaves the fixing
// to doctor, never starts a proxy just to look at one, and pays only
// for what the registry already holds.
func (m *Machine) Status(ctx context.Context, dir string, streams IO) (problems int, err error) {
	d, err := m.diagnose(ctx, dir, fromRegistry)
	if err != nil {
		return 0, err
	}
	return report.Dashboard(streams.Out, streams.OutStyle, d), nil
}

// verdict is the one line that says whether anything of this device is
// captured right now, for the project at dir. enable ends with it and
// status leads with it. It is read from the same diagnosis the
// dashboard renders, so the line enable prints cannot differ from the
// line status opens with. A diagnosis that cannot be resolved reads as
// a project that does not contribute: the line must still answer the
// question. The state comes with the line so a caller can act on it
// without parsing the text.
func (m *Machine) verdict(ctx context.Context, dir string) (report.RecordingState, string) {
	d, err := m.diagnose(ctx, dir, fromRegistry)
	if err != nil {
		return report.RecordingOffHere, report.Verdict(report.RecordingOffHere, 0)
	}
	state := report.Recording(d)
	return state, report.Verdict(state, d.EnabledProjects)
}
