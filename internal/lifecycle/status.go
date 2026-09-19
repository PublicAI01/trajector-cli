package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/report"
)

// Status prints the device dashboard. It resolves the diagnosis and
// hands it to the renderer: status repairs nothing, always leaves the
// fixing to doctor, never starts a proxy just to look at one, and pays
// only for what the registry already holds.
func (m *Machine) Status(dir string, io IO) error {
	d, err := m.diagnose(dir, fromRegistry)
	if err != nil {
		return err
	}
	report.Dashboard(io.Out, d)
	return nil
}
