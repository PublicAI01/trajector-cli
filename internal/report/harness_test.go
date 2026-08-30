package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/report"
)

// The whole of this suite is one value in and text out: no store is
// opened, no process is started, and nothing is repaired, so a wording
// change is answered in milliseconds.

// spoolDir and rejectedDir stand in for a device's directories, which
// several sentences name.
const (
	spoolDir    = "/home/dev/.local/share/trajector/spool"
	rejectedDir = "/home/dev/.local/share/trajector/upload/rejected"
)

// device is a healthy paired device with nothing waiting, the value
// every case below varies one fact of.
func device() report.Diagnosis {
	return report.Diagnosis{
		Version: "testv",
		Project: report.ProjectStatus{Root: "/home/dev/sample-project"},
		Spool: report.SpoolState{
			Dir:   spoolDir,
			Quota: 2 << 30,
		},
		RejectedDir: rejectedDir,
		TokenStore:  report.TokenStoreState{Paired: true},
	}
}

// dashboard is what `trajector status` prints for a diagnosis.
func dashboard(d report.Diagnosis) string {
	var b bytes.Buffer
	report.Dashboard(&b, d)
	return b.String()
}

// doctorText is what `trajector doctor` prints for a diagnosis, and how
// many problems it counts, from the sections a diagnosis alone
// establishes — the repairs are the machine's and are asserted where
// they run.
func doctorText(d report.Diagnosis) (int, string) {
	f := &report.Findings{}
	report.DoctorDevice(f, d)
	report.DoctorData(f, d)
	report.DoctorEnvironment(f)
	var b bytes.Buffer
	f.Render(&b)
	return f.Problems(), b.String()
}

// wants asserts every fragment is present in one surface's output.
func wants(t *testing.T, surface, out string, fragments ...string) {
	t.Helper()
	for _, want := range fragments {
		if !strings.Contains(out, want) {
			t.Errorf("%s = %q, want it to contain %q", surface, out, want)
		}
	}
}

// rejects asserts no fragment appears in one surface's output.
func rejects(t *testing.T, surface, out string, fragments ...string) {
	t.Helper()
	for _, unwanted := range fragments {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s = %q, want no %q", surface, out, unwanted)
		}
	}
}

// detailsUnder is the follow-up lines doctor printed under one finding,
// which is where every remedy lands.
func detailsUnder(printed, finding string) ([]string, bool) {
	lines := strings.Split(printed, "\n")
	for i, line := range lines {
		if line != finding {
			continue
		}
		var details []string
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "      ") {
				break
			}
			details = append(details, next)
		}
		return details, true
	}
	return nil, false
}
