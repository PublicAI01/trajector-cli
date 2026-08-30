package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/report"
)

// Status prints the device dashboard. It resolves the diagnosis and
// hands it to the renderer: status repairs nothing, always leaves the
// fixing to doctor, and never starts a proxy just to look at one.
func (m *Machine) Status(dir string, io IO) error {
	d, err := m.Diagnose(dir)
	if err != nil {
		return err
	}
	report.Dashboard(io.Out, d)
	return nil
}
