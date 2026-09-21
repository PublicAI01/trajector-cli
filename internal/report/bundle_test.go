package report_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
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

// A holder that engaged the admin-token challenge without proving a
// token is most often this user's own proxy whose publication went
// missing, so the archive names no process for it: a process id and a
// program name there read as a foreign process to go and stop.
func TestTheBundleNamesNoHolderForAProxyItCouldNotVerify(t *testing.T) {
	d := device()
	d.Proxy = foreign(proxylife.ErrProxyUnverified)
	d.ProxyHolder = report.HolderProcess{PID: 4321, Name: "example-helper"}

	rejects(t, "diagnosis.json", string(report.DiagnosisJSON(d)),
		"holder_pid", "holder_name", "4321", "example-helper")
}

func TestTheBundleNamesTheHolderOfAPortHeldAgainstThisBuild(t *testing.T) {
	d := device()
	d.Proxy = foreign(&proxylife.PortHeld{Addr: "127.0.0.1:41100", Why: proxylife.ErrPortSilent})
	d.ProxyHolder = report.HolderProcess{PID: 4321, Name: "example-helper"}

	wants(t, "diagnosis.json", string(report.DiagnosisJSON(d)),
		`"holder_pid": 4321`, `"holder_name": "example-helper"`)
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

func TestTheBundleExplainsAPauseTheWayTheOtherSurfacesDo(t *testing.T) {
	d := device()
	d.Project.PauseReason = routing.PauseConsentUnreadable
	d.Project.ConsentPath = "/home/dev/.config/trajector/consent.json"
	d.Project.ConsentErr = errors.New("unexpected end of JSON input")

	wants(t, "diagnosis.json", string(report.DiagnosisJSON(d)),
		`"pause_reason": "consent_unreadable"`,
		`"pause_explanation": "the consent record at /home/dev/.config/trajector/consent.json could not be read (unexpected end of JSON input)`,
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

// bundleOmits names each fact a Diagnosis holds and the bundle does not
// carry as a value of its own, by the path from the Diagnosis down to
// it, with why the archive stays answerable without it.
var bundleOmits = map[string]string{
	"RejectedDir":             "the directory is this build's own layout, not an observation about the device, and the batches waiting in it are carried in full",
	"Spool.Dir":               "the directory is this build's own layout, not an observation about the device, and what waits in it is carried in full",
	"Project.Shape":           "the grant records one of two shapes, and which of the two it is is carried whole as no_proxy",
	"Project.GrantHash":       "the identity the routing table records for the grant is carried in the routing table the bundle archives beside this file, where a reader compares it with project_id_hash",
	"Project.Hooks":           "which hooks stand in the settings file is carried as one flag per hook this release installs; a name this release never installs is not one of them",
	"Project.InjectedBaseURL": "the injected base URL addresses this device's own proxy, whose address the bundle names, and the token it carries is carried masked",
	"Project.InjectionAgrees": "whether the injection is the one the grant calls for is a comparison of the granted and the injected token, and the bundle carries both",
}

// bundleContext is what a fact says nothing without. Such a fact is
// asserted against a bundle written with its context alone, so what the
// subtest measures is still the one fact it names.
var bundleContext = map[string]func(*report.Diagnosis){
	"Proxy.Health": func(d *report.Diagnosis) {
		d.Proxy.Holder = proxylife.HolderOurs
	},
	"ProxyHolder": func(d *report.Diagnosis) {
		d.Proxy.Reason = &proxylife.PortHeld{Addr: "127.0.0.1:41100", Why: proxylife.ErrPortOccupied}
	},
	"Project.UpstreamMoved.From": func(d *report.Diagnosis) {
		d.Project.UpstreamMoved.At = "2026-08-01T09:00:00Z"
	},
	"Project.ConsentErr": func(d *report.Diagnosis) {
		d.Project.PauseReason = routing.PauseConsentUnreadable
	},
	"Project.ConsentPath": func(d *report.Diagnosis) {
		d.Project.PauseReason = routing.PauseConsentUnreadable
		d.Project.ConsentErr = errors.New("permission denied")
	},
}

// contextFor is the context a fact needs, named on the fact itself or
// on a value it is part of, the nearest of them winning.
func contextFor(fact string) func(*report.Diagnosis) {
	for at := fact; ; {
		if context, needs := bundleContext[at]; needs {
			return context
		}
		cut := strings.LastIndex(at, ".")
		if cut < 0 {
			return nil
		}
		at = at[:cut]
	}
}

func TestTheBundleCarriesEveryFactADiagnosisHolds(t *testing.T) {
	generatedAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	written := func(d report.Diagnosis) string {
		return string(report.DiagnosisJSON(d)) + string(report.InfoJSON(d, generatedAt))
	}

	for _, fact := range factsOf(t, reflect.TypeFor[report.Diagnosis](), "", nil) {
		t.Run(fact, func(t *testing.T) {
			if why, omitted := bundleOmits[fact]; omitted {
				t.Skip(why)
			}
			var beside report.Diagnosis
			if context := contextFor(fact); context != nil {
				context(&beside)
			}
			d := beside
			fillForTheBundle(t, factAt(t, reflect.ValueOf(&d).Elem(), fact))
			if got := written(d); got == written(beside) {
				t.Errorf("%s reached nothing in the bundle:\n%s", fact, got)
			}
		})
	}
}

// factsOf lists what a diagnosis knows as one path per fact: a struct
// is not a fact, the leaves under it are. A type that holds itself
// stops the descent, as does a struct no caller can fill.
func factsOf(t *testing.T, at reflect.Type, prefix string, holding []reflect.Type) []string {
	t.Helper()
	for at.Kind() == reflect.Pointer {
		at = at.Elem()
	}
	fact := strings.TrimSuffix(prefix, ".")
	if at.Kind() != reflect.Struct || at == reflect.TypeFor[time.Time]() || slices.Contains(holding, at) {
		return []string{fact}
	}
	var facts []string
	for i := range at.NumField() {
		field := at.Field(i)
		if field.PkgPath != "" {
			continue
		}
		facts = append(facts, factsOf(t, field.Type, prefix+field.Name+".", append(holding, at))...)
	}
	if len(facts) == 0 {
		return []string{fact}
	}
	return facts
}

// factAt resolves a fact's path to the field holding it, allocating
// every pointer on the way down.
func factAt(t *testing.T, v reflect.Value, fact string) reflect.Value {
	t.Helper()
	for name := range strings.SplitSeq(fact, ".") {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.FieldByName(name)
		if !v.IsValid() {
			t.Fatalf("%s names no field in a diagnosis", fact)
		}
	}
	return v
}

// fillForTheBundle gives a field a value no zero Diagnosis has, so that
// a field the bundle drops leaves the archive unchanged. A number is
// filled with the value next to zero: a type that names its values
// renders one it does not name as the name it gives zero, and such a
// field would then read as dropped.
func fillForTheBundle(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.String:
		v.SetString("carried")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
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
