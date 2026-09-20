package report_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func TestDoctorExplainsADeviceWidePause(t *testing.T) {
	d := device()
	d.Project.PauseReason = routing.PauseSignedOut

	problems, out := doctorText(d)
	if problems == 0 {
		t.Error("doctor found no problem on a paused device")
	}
	wants(t, "doctor", out, "trajector login")
}

func TestDoctorReportsAnUnreadableTokenStore(t *testing.T) {
	// The pairing state is now unknown, which must never present as
	// signed out.
	d := device()
	d.TokenStore = report.TokenStoreState{Err: errors.New("is a directory")}

	problems, out := doctorText(d)
	if problems == 0 {
		t.Error("doctor found no problem with an unreadable token store")
	}
	wants(t, "doctor", out, "token store could not be read", "pairing state is unknown")
}

// An optional setting left off is not a fault, so doctor never
// mentions one, whatever state it is in.
func TestDoctorSaysNothingAboutOptionalSettings(t *testing.T) {
	for _, state := range []claudesettings.SettingState{
		claudesettings.Unset, claudesettings.OnByUs, claudesettings.OnByUser, claudesettings.OffByUser,
	} {
		for _, declined := range []bool{false, true} {
			d := device()
			d.Project = contributing()
			d.OptionalSettings = []report.OptionalSettingStatus{
				{Key: claudesettings.KeyShowThinkingSummaries, State: state, Declined: declined},
			}
			_, out := doctorText(d)

			rejects(t, "doctor", out, "optional", "Optional", claudesettings.KeyShowThinkingSummaries)
		}
	}
}

func TestDoctorListsRejectedBatches(t *testing.T) {
	d := device()
	d.Rejected = []upload.RejectedBatch{{
		BatchID: "b-poison",
		Records: 1,
		Reason:  upload.Rejection{Details: "413 Request Entity Too Large"},
	}}

	problems, out := doctorText(d)
	if problems == 0 {
		t.Fatalf("problems = 0 with a quarantined batch, output:\n%s", out)
	}
	wants(t, "doctor", out, "b-poison", "413 Request Entity Too Large", "requeue", "discard")
}

// Records this machine set aside as unreadable can never re-enter the
// spool, so offering requeue for them would send the user to a command
// that refuses.
func TestDoctorOffersNoRequeueForRecordsItSetAsideItself(t *testing.T) {
	d := device()
	d.Rejected = []upload.RejectedBatch{{
		BatchID: "b-unreadable",
		Records: 1,
		Reason:  upload.Rejection{Cause: upload.CauseUnreadable},
	}}

	_, out := doctorText(d)
	wants(t, "doctor", out, "never sent: unreadable in the spool", "cannot be requeued")
	rejects(t, "doctor", out, "to upload them again")
}

func TestDoctorReportsRejectedBatchesItCannotRead(t *testing.T) {
	d := device()
	d.RejectedErr = errors.New("not a directory")

	problems, out := doctorText(d)
	if problems == 0 {
		t.Fatalf("problems = 0 with an unreadable quarantine, output:\n%s", out)
	}
	wants(t, "doctor", out, "the rejected batches at "+rejectedDir+" could not be read")
}

// The one shape doctor uses for every reason uploads are held back: the
// standing's own sentence as the finding, then whatever the service
// said, then the standing's own remedy. A user who has learned to read
// one of the three gates can read the other two.
func TestDoctorExplainsEveryUploadGateInTheSameShape(t *testing.T) {
	until := time.Date(2026, 8, 2, 14, 32, 0, 0, time.UTC)
	d := device()
	d.Standings = []upload.Standing{
		{Reason: upload.VersionGate, MinClientVersion: "9.9.9", Version: "0.1.0",
			Message: "Upload format 0.1.x is retired on 2026-09-01.", Upgradable: true},
		{Reason: upload.AuthorizationGate, AuthorizeURL: "https://dashboard.example.com/authorization",
			Message: "Your data authorization is not complete."},
		{Reason: upload.RateLimited, NotBefore: until},
	}

	problems, out := doctorText(d)
	if problems != 0 {
		t.Fatalf("problems = %d, want three pauses reported without failing doctor, output:\n%s", problems, out)
	}
	for _, gate := range []struct{ name, finding, remedy string }{
		{
			name:    "426",
			finding: "the service requires client version 9.9.9 or newer; this build is 0.1.0",
			remedy:  "Run `trajector upgrade` to install the newest release.",
		},
		{
			name:    "451",
			finding: "uploads are paused: this account's data authorization is not complete",
			remedy:  "Complete your data authorization at https://dashboard.example.com/authorization — then uploads resume.",
		},
		{
			name:    "429",
			finding: "uploads are paused until " + until.Format(time.RFC3339) + ": the service asked to slow down",
			remedy:  "Uploads resume automatically; `trajector upload --force` offers them now.",
		},
	} {
		t.Run(gate.name, func(t *testing.T) {
			details, ok := detailsUnder(out, "  note: "+gate.finding)
			if !ok {
				t.Fatalf("doctor = %q, want the finding %q", out, gate.finding)
			}
			if !slices.Contains(details, "      "+gate.remedy) {
				t.Errorf("details under %q = %q, want the remedy %q", gate.finding, details, gate.remedy)
			}
		})
	}
}

// Nothing here is broken and nothing doctor can do would change it: the
// user finishes this in a browser. Counting it as a problem would fail
// `trajector doctor` on a healthy install.
func TestDoctorRelaysAPausedUploaderWithoutCallingTheMachineBroken(t *testing.T) {
	d := device()
	d.Standings = []upload.Standing{{
		Reason:       upload.AuthorizationGate,
		AuthorizeURL: "https://dashboard.example.com/authorization",
		Message:      "Your data authorization is not complete.",
	}}

	problems, out := doctorText(d)
	if problems != 0 {
		t.Fatalf("problems = %d, want a paused uploader reported without failing doctor, output:\n%s", problems, out)
	}
	wants(t, "doctor", out,
		"data authorization is not complete",
		"Your data authorization is not complete.",
		"https://dashboard.example.com/authorization",
		"Captured data is kept",
	)
}

func TestDoctorRelaysAServiceNoticeWithoutCallingItAFault(t *testing.T) {
	d := device()
	d.Handshake.Notice = "maintenance on Friday"

	problems, out := doctorText(d)
	if problems != 0 {
		t.Fatalf("problems = %d, want a notice relayed without affecting the exit code, output:\n%s", problems, out)
	}
	wants(t, "doctor", out, "maintenance on Friday")
}

// One diagnosis, two surfaces: the sentence about a spool that cannot
// be used has to be the same on both, which is only checkable by
// rendering the same value twice.
func TestStatusAndDoctorPresentAnUnusableSpoolAlike(t *testing.T) {
	d := device()
	d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: errors.New("not a directory")}
	want := "the capture spool at " + spoolDir + " is not usable"

	problems, doctorOut := doctorText(d)
	if problems == 0 {
		t.Fatalf("problems = 0 with an unusable spool, output:\n%s", doctorOut)
	}
	wants(t, "status", dashboard(d), want)
	wants(t, "doctor", doctorOut, want)
}

func TestStatusAndDoctorPresentAFullSpoolAlike(t *testing.T) {
	d := device()
	d.Spool = full()

	problems, doctorOut := doctorText(d)
	if problems == 0 {
		t.Fatalf("problems = 0 with a full spool, output:\n%s", doctorOut)
	}
	for surface, out := range map[string]string{"status": dashboard(d), "doctor": doctorOut} {
		wants(t, surface, out,
			"recording is stopped: the capture spool is full",
			"fix:  trajector upload --force")
	}
}

func TestStatusAndDoctorPresentAnUnwritableSpoolAlike(t *testing.T) {
	d := device()
	d.Spool.WritableErr = errors.New("permission denied")

	problems, doctorOut := doctorText(d)
	if problems == 0 {
		t.Fatalf("problems = 0 with an unwritable spool, output:\n%s", doctorOut)
	}
	for surface, out := range map[string]string{"status": dashboard(d), "doctor": doctorOut} {
		wants(t, surface, out, "not writable, so recording is stopped")
		// No quota remedy for a spool that is not full.
		rejects(t, surface, out, "the capture spool is full", "trajector upload --force")
	}
}

// A full spool is what a refused endpoint leads to when nothing
// uploads for long enough, so the two stand together and both surfaces
// have to say both: the spool that stopped recording, and the refusal
// that filled it.
func TestStatusAndDoctorReportAFullSpoolBesideARefusedEndpoint(t *testing.T) {
	at := time.Date(2026, 8, 30, 14, 32, 0, 0, time.UTC)
	d := device()
	d.Spool = full()
	d.Standings = []upload.Standing{{
		Reason:    upload.AccessRefused,
		Since:     at,
		NotBefore: at.Add(time.Minute),
	}}

	problems, doctorOut := doctorText(d)
	if problems == 0 {
		t.Fatalf("problems = 0 with a full spool and a refused endpoint, output:\n%s", doctorOut)
	}
	for surface, out := range map[string]string{"status": dashboard(d), "doctor": doctorOut} {
		wants(t, surface, out,
			"fix:  trajector upload --force",
			// Doctor states the clause in its own casing, so both
			// surfaces are held to the clause itself.
			"paused since 2026-08-30T14:32:00Z until 2026-08-30T14:33:00Z: the service refused this client access")
	}
}

// Everything a surface offers as a fix, in every shape a device can
// reach one: a pause of each kind, a store and a spool that refuse, a
// quarantine, a project short a hook, and a port this device could not
// take.
func everyFixOffered() []string {
	var printed []string
	surfaces := func(d report.Diagnosis) {
		f := &report.Findings{}
		report.DoctorDevice(f, d)
		report.DoctorProject(f, d)
		report.DoctorData(f, d)
		var b bytes.Buffer
		f.Render(&b, report.Style{})
		printed = append(printed, b.String(), dashboard(d))
	}
	for _, reason := range routing.AllPauseReasons() {
		d := enabledDevice()
		d.Project.PauseReason = reason
		d.Project.ConsentPath, d.Project.ConsentErr = "/home/dev/consent.json", errors.New("unexpected end of JSON input")
		surfaces(d)
	}
	unreadableStore := device()
	unreadableStore.TokenStore.Err = errors.New("the keyring is locked")
	surfaces(unreadableStore)

	fullSpool := device()
	fullSpool.Spool.Usage, fullSpool.Spool.WritableErr = fullSpool.Spool.Quota, spool.ErrQuotaExceeded
	surfaces(fullSpool)

	quarantined := device()
	quarantined.Rejected = []upload.RejectedBatch{{BatchID: "b-1", Records: 2}}
	surfaces(quarantined)

	shortAHook := enabledDevice()
	shortAHook.Project.Injected = true
	shortAHook.Project.Hooks = claudesettings.InstalledHooks{claudesettings.HookEnsureProxy}
	shortAHook.StaleDiscoveryHook = true
	shortAHook.Project.WindowsSideClaude = true
	surfaces(shortAHook)

	foreignPort := device()
	foreignPort.Proxy.Holder, foreignPort.Proxy.Reason = proxylife.HolderForeign, proxylife.ErrPortOccupied
	f := &report.Findings{}
	f.ProxyProblem(foreignPort.Proxy.Reason)
	var b bytes.Buffer
	f.Render(&b, report.Style{})
	printed = append(printed, b.String(), dashboard(foreignPort))

	var fixes []string
	for _, out := range printed {
		for _, line := range strings.Split(out, "\n") {
			if command, ok := strings.CutPrefix(line, "    fix:  "); ok {
				fixes = append(fixes, command)
			}
		}
	}
	return fixes
}

func TestEveryFixLineIsACommandAUserCanCopyWhole(t *testing.T) {
	fixes := everyFixOffered()
	if len(fixes) < len(routing.AllPauseReasons()) {
		t.Fatalf("%d fix line(s) offered, want at least one for every pause", len(fixes))
	}
	for _, fix := range fixes {
		command := fix
		if key, rest, ok := strings.Cut(fix, "="); ok && !strings.Contains(key, " ") {
			_, command, _ = strings.Cut(rest, " ")
		}
		if !strings.HasPrefix(command, "trajector ") {
			t.Errorf("fix line %q, want the command itself or one environment variable before it", fix)
		}
		if strings.ContainsAny(fix, "`()") {
			t.Errorf("fix line %q, want nothing on it but the command", fix)
		}
	}
}
