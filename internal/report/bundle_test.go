package report_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// One serialized Diagnosis carries what used to be scattered over prose
// and per-surface files: project, live proxy report, spool, and the
// rejected batch's recorded reason.
func TestTheBundleSerializesEveryPartOfADiagnosis(t *testing.T) {
	d := device()
	d.Project = contributing()
	d.Proxy = ours("testv")
	d.Spool.Usage = 4096
	d.Rejected = []upload.RejectedBatch{{
		BatchID: "b-poison",
		Records: 1,
		Reason:  upload.Rejection{Details: "413 Request Entity Too Large"},
	}}
	d.Standings = []upload.Standing{{
		Reason:           upload.VersionGate,
		MinClientVersion: "9.9.9",
		Version:          "0.1.0",
		Message:          "Upload format 0.1.x is retired on 2026-09-01.",
		Upgradable:       true,
	}}

	got := string(report.DiagnosisJSON(d))
	wants(t, "diagnosis.json", got,
		`"hooks_installed": true`,
		`"holder": "ours"`,
		`"service": "trajector-proxy"`,
		`"usage_bytes"`,
		`"413 Request Entity Too Large"`,
		`"min_client_version": "9.9.9"`,
		// Support reads why uploads were held back as the client itself
		// judged it, with the service's own words beside the judgement:
		// "this client is behind" and "the service wants something else"
		// are different reports and must not have to be told apart by
		// re-deriving anything from the handshake.
		`"reason": "version_gate"`,
		`"message": "Upload format 0.1.x is retired on 2026-09-01."`,
	)
}

// A store that could not be read is a fact the bundle carries, not a
// reason to write no bundle.
func TestTheBundleRecordsStoreFailures(t *testing.T) {
	d := device()
	d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: errors.New("not a directory")}
	d.RejectedErr = errors.New("not a directory")

	wants(t, "diagnosis.json", string(report.DiagnosisJSON(d)), `"open_err"`, `"rejected_err"`)
}

// Tokens are masked by the type that carries them, so a token field
// added to the rendering is masked by construction.
func TestTheBundleNeverCarriesATokenInTheClear(t *testing.T) {
	d := device()
	d.Project = contributing()
	d.Project.Token = "0123456789abcdef0123456789abcdef"
	d.Project.InjectedToken = d.Project.Token

	got := string(report.DiagnosisJSON(d))
	if strings.Contains(got, d.Project.Token) {
		t.Errorf("diagnosis.json = %s\nwant the project token masked", got)
	}
	wants(t, "diagnosis.json", got, "01234567…(masked)")
}

// A proxy that is not ours has no health to report, and saying nothing
// about it is not the same as reporting a zeroed one.
func TestTheBundleReportsAnUnprovenHoldersReasonInsteadOfItsHealth(t *testing.T) {
	d := device()
	d.Proxy = foreign(errors.New("port occupied by a process that is not the trajector proxy"))

	got := string(report.DiagnosisJSON(d))
	wants(t, "diagnosis.json", got, `"holder": "foreign"`, `"reason": "port occupied`)
	rejects(t, "diagnosis.json", got, `"health"`)
}

func TestTheBundleNamesTheBuildAndTheProxyItDiagnosed(t *testing.T) {
	d := device()
	d.Proxy = ours("testv")
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	wants(t, "info.json", string(report.InfoJSON(d, at)),
		`"version": "testv"`,
		`"proxy_addr": "127.0.0.1:41100"`,
		`"generated_at": "2026-08-02T12:00:00Z"`,
	)
}
