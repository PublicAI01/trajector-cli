package report_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
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

func TestTheBundleCarriesWhatTheOtherSurfacesReportAboutTheDevice(t *testing.T) {
	d := device()
	d.Project = contributing()
	d.StaleDiscoveryHook = true
	d.ProxyIdleBetweenSessions = true
	d.OptionalSettings = []report.OptionalSettingStatus{{
		Key:      claudesettings.KeyShowThinkingSummaries,
		State:    claudesettings.OffByUser,
		Declined: true,
	}}

	wants(t, "diagnosis.json", string(report.DiagnosisJSON(d)),
		`"stale_discovery_hook": true`,
		`"idle_between_sessions": true`,
		`"`+claudesettings.KeyShowThinkingSummaries+`"`,
		`"state": "off_by_user"`,
		`"declined": true`,
	)
}

func TestTheBundleCountsWhatTheSpoolHoldsBack(t *testing.T) {
	d := device()
	d.Spool.Held = report.HeldRecords{Records: 3, Sessions: 2, Bytes: 4096}

	wants(t, "diagnosis.json", string(report.DiagnosisJSON(d)),
		`"records": 3`,
		`"sessions": 2`,
		`"bytes": 4096`,
	)
}

// bundleOmits names each Diagnosis field the bundle leaves out, with
// why leaving it out keeps the archive answerable.
var bundleOmits = map[string]string{
	"RejectedDir": "the directory is this build's own layout, not an observation about the device, and the batches waiting in it are carried in full",
}

func TestTheBundleCarriesEveryFactADiagnosisHolds(t *testing.T) {
	generatedAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	written := func(d report.Diagnosis) string {
		return string(report.DiagnosisJSON(d)) + string(report.InfoJSON(d, generatedAt))
	}
	nothing := written(report.Diagnosis{})

	diagnosis := reflect.TypeFor[report.Diagnosis]()
	for i := range diagnosis.NumField() {
		field := diagnosis.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			if why, omitted := bundleOmits[field.Name]; omitted {
				t.Skip(why)
			}
			var d report.Diagnosis
			fillForTheBundle(t, reflect.ValueOf(&d).Elem().Field(i))
			if got := written(d); got == nothing {
				t.Errorf("%s reached nothing in the bundle:\n%s", field.Name, got)
			}
		})
	}
}

// fillForTheBundle gives a field a value no zero Diagnosis has, so that
// a field the bundle drops leaves the archive unchanged.
func fillForTheBundle(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.String:
		v.SetString("carried")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Pointer:
		held := reflect.New(v.Type().Elem())
		fillForTheBundle(t, held.Elem())
		v.Set(held)
	case reflect.Slice:
		held := reflect.New(v.Type().Elem())
		fillForTheBundle(t, held.Elem())
		v.Set(reflect.Append(v, held.Elem()))
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillForTheBundle(t, key)
		held := reflect.New(v.Type().Elem()).Elem()
		fillForTheBundle(t, held)
		filled := reflect.MakeMap(v.Type())
		filled.SetMapIndex(key, held)
		v.Set(filled)
	case reflect.Interface:
		if !v.Type().Implements(reflect.TypeFor[error]()) {
			t.Fatalf("no value for a %s field; teach this test the type", v.Type())
		}
		v.Set(reflect.ValueOf(errors.New("carried")))
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)))
			return
		}
		for i := range v.NumField() {
			if v.Field(i).CanSet() {
				fillForTheBundle(t, v.Field(i))
			}
		}
	default:
		t.Fatalf("no value for a %s field; teach this test the kind", v.Kind())
	}
}
