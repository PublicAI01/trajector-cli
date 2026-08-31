package lifecycle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

func (e *env) statusOutput() string {
	e.t.Helper()
	if err := e.machine().Status(e.project, e.io()); err != nil {
		e.t.Fatalf("status: %v\nstdout: %s", err, e.stdout)
	}
	return e.stdout.String()
}

// The one pass over the whole path: a real enable against a real proxy,
// read back by a real diagnosis. What each line says is settled where
// the renderer lives; this is that the renderer is handed the truth.
func TestStatusShowsTheStateOfADeviceThatJustEnabledAProject(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	e.stdout.Reset()
	out := e.statusOutput()

	for _, want := range []string{
		"Signed in",
		"Contributing",
		"Running at " + e.deps.ProxyAddr,
		"version testv",
		// Named for the span it counts — this proxy's run, the one the
		// uptime on the line above measures — not for a calendar day
		// no restart respects.
		"Recorded since it started: 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "third-party") {
		t.Errorf("official upstream labelled third-party: %q", out)
	}
}

// What the optional-settings line says is settled at the renderer;
// this is that the diagnosis hands it the state enable left behind.
func TestStatusCarriesTheOptionalSettingAnswerEnableRecorded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seed   func(t *testing.T, e *env)
		stdin  string
		want   string
		reject string
	}{
		{
			name:  "accepted and written by trajector",
			stdin: "yes\ny\n",
			want:  "Optional settings: " + optionalKey + " on (set by trajector).",
		},
		{
			name:   "declined once",
			stdin:  "yes\nn\n",
			want:   "Optional settings: 1 declined. Run `trajector enable` to review.",
			reject: "costs you nothing",
		},
		{
			name: "on as the user's own choice",
			seed: func(t *testing.T, e *env) {
				writeUserSettings(t, e, `{"showThinkingSummaries": true}`)
			},
			stdin:  "yes\n",
			want:   "Optional settings: " + optionalKey + " on.",
			reject: "set by trajector",
		},
		{
			// stdin ends before the question, so nothing was answered,
			// nothing recorded: status still recommends.
			name:  "left off with no answer recorded",
			stdin: "yes\n",
			want: "One optional setting is off: " + optionalKey + ". Turning it on costs " +
				"you nothing and makes your records more complete. Run `trajector enable` " +
				"to see what it changes.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			if tc.seed != nil {
				tc.seed(t, e)
			}
			e.stdin = tc.stdin
			if err := e.machine().Enable(e.project, e.io()); err != nil {
				t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
			}
			e.stdout.Reset()
			out := e.statusOutput()

			if !strings.Contains(out, tc.want) {
				t.Errorf("status = %q, want it to contain %q", out, tc.want)
			}
			if tc.reject != "" && strings.Contains(out, tc.reject) {
				t.Errorf("status = %q, want no %q", out, tc.reject)
			}
		})
	}
}

func TestStatusShowsNoOptionalSettingLineForAProjectNotEnabled(t *testing.T) {
	e := newEnv(t)
	out := e.statusOutput()

	for _, unwanted := range []string{"Optional settings", "optional setting", optionalKey} {
		if strings.Contains(out, unwanted) {
			t.Errorf("status = %q, want no %q for a project that is not enabled", out, unwanted)
		}
	}
}

func TestStatusReportsAHealthzCopyingPortHolder(t *testing.T) {
	e := newEnv(t)
	im := e.occupyPortWithHealthzCopy()
	out := e.statusOutput()

	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "not the trajector proxy") {
		t.Errorf("status = %q, want a loud warning for a holder that only copies the health payload", out)
	}
	if strings.Contains(out, "Running at") {
		t.Errorf("status = %q, want no running-proxy section for an unproven holder", out)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

func TestStatusDoesNotWaitOutAHolderStillPublishingItsToken(t *testing.T) {
	e := newEnv(t)
	im := e.occupyPortStillPublishing()
	out := e.statusOutput()

	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "not the trajector proxy") {
		t.Errorf("status = %q, want the unproven holder reported as it stands", out)
	}
	if strings.Contains(out, "Running at") {
		t.Errorf("status = %q, want no running-proxy section before the holder proves itself", out)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

func TestStatusAnswersAboutAnUnprovenHolderWithoutPayingTheStartupGrace(t *testing.T) {
	e := newEnv(t)
	e.occupyPortWithHealthzCopy()

	start := time.Now()
	e.statusOutput()
	reported := time.Since(start)

	p := proxylife.For(e.layout(), e.deps.Version, "unused", e.deps.ProxyAddr)
	start = time.Now()
	p.Settled()
	settled := time.Since(start)

	if reported*2 >= settled {
		t.Errorf("status answered in %s where a settled verdict about the same holder takes %s; only callers about to act pay the startup grace", reported, settled)
	}
}

func TestStatusWarnsWhenTheSpoolIsFull(t *testing.T) {
	e := newEnv(t)
	e.sandbox.SeedHandshake(proxytest.Handshake{SpoolQuotaBytes: 1})
	e.sandbox.SeedRawcall("req-1", "hash-project", e.deps.Now())
	out := e.statusOutput()

	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "full") {
		t.Errorf("status = %q, want a loud spool-full warning", out)
	}
}

func TestStatusReportsRecordingStoppedByAnUnwritableSpool(t *testing.T) {
	e := newEnv(t)
	readOnly(t, e.layout().SpoolDir())
	out := e.statusOutput()

	for _, want := range []string{"WARNING", "not writable, so recording is stopped", "`trajector doctor`"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "is not usable") {
		t.Errorf("status = %q, want a spool that opened fine reported as unwritable, not unusable", out)
	}
	for _, want := range []string{"Uploads", "Never uploaded"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want the sections after the spool still rendered (%q)", out, want)
		}
	}
}

func TestStatusShowsServiceMinVersionAndNotice(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0" // behind the minimum below
	e.sandbox.SeedHandshake(proxytest.Handshake{
		MinClientVersion: "9.9.9",
		Notice:           "scheduled maintenance on Friday",
	})
	out := e.statusOutput()

	for _, want := range []string{"9.9.9", "0.1.0", "scheduled maintenance on Friday", "trajector upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
}

func TestStatusReportsAPausedUploaderWaitingOnDataAuthorization(t *testing.T) {
	// Without this the uploader is simply silent: nothing uploads, no
	// error is shown, and the user has no way to learn that the thing
	// stopping it is something they finish in a browser.
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	e.sandbox.SeedHandshake(proxytest.Handshake{MinClientVersion: "0.1.0"})
	e.sandbox.SeedAuthorizationRefusal(
		"https://dashboard.example.com/authorization",
		"Your data authorization is not complete.")
	out := e.statusOutput()

	for _, want := range []string{
		"data authorization is not complete",
		"Your data authorization is not complete.",
		"https://dashboard.example.com/authorization",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	// A satisfied minimum must not drag the upgrade lines in with it.
	if strings.Contains(out, "trajector upgrade") {
		t.Errorf("status = %q, want no upgrade instruction for a build that meets the minimum", out)
	}
}

func TestStatusDropsAnAuthorizeAddressAUserCannotSafelyOpen(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	e.sandbox.SeedAuthorizationRefusal("http://dashboard.example.com/authorization", "")
	out := e.statusOutput()

	if strings.Contains(out, "http://dashboard.example.com") {
		t.Errorf("status = %q, want the unusable address dropped", out)
	}
	if !strings.Contains(out, "Trajector dashboard") {
		t.Errorf("status = %q, want wording of our own in its place", out)
	}
}

// The service announces its minimum on every acknowledgement, so a
// build that meets it would otherwise carry the requirement and the
// instruction to upgrade in every status for good — and be reading the
// same two lines on the one occasion they mean something.
func TestStatusSaysNothingAboutAMinimumThisBuildMeets(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	for _, minimum := range []string{"0.1.0", "0.0.9"} {
		e.stdout.Reset()
		e.sandbox.SeedHandshake(proxytest.Handshake{MinClientVersion: minimum})
		out := e.statusOutput()

		for _, unwanted := range []string{"trajector upgrade", "requires client version", "\nService"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("status with minimum %s on build 0.1.0 = %q, want no %q", minimum, out, unwanted)
			}
		}
	}
}

// A satisfied minimum silences the version lines, not the whole block:
// a notice is the service talking about something else entirely.
func TestStatusStillRelaysANoticeWhenTheVersionIsFine(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.2.0"
	e.sandbox.SeedHandshake(proxytest.Handshake{
		MinClientVersion: "0.1.0",
		Notice:           "scheduled maintenance on Friday",
	})
	out := e.statusOutput()

	if !strings.Contains(out, "scheduled maintenance on Friday") {
		t.Errorf("status = %q, want the notice relayed", out)
	}
	if strings.Contains(out, "trajector upgrade") {
		t.Errorf("status = %q, want no upgrade instruction for a build that meets the minimum", out)
	}
}

// Not knowing is not the same as being behind. A dev build cannot be
// ordered against a release, so the requirement is stated and the
// remedy is not — upgrade has nothing to install for one.
func TestStatusStatesAnUnorderableMinimumWithoutSendingTheUserToUpgrade(t *testing.T) {
	for _, tc := range []struct{ name, version, minimum string }{
		{"dev build", "dev", "0.1.0"},
		{"minimum is not a version", "0.1.0", "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.deps.Version = tc.version
			e.sandbox.SeedHandshake(proxytest.Handshake{MinClientVersion: tc.minimum})
			out := e.statusOutput()

			if !strings.Contains(out, tc.minimum) || !strings.Contains(out, tc.version) {
				t.Errorf("status = %q, want both versions stated", out)
			}
			if strings.Contains(out, "trajector upgrade") {
				t.Errorf("status = %q, want no upgrade instruction on an unorderable pair", out)
			}
		})
	}
}

func TestStatusRelaysWhatTheServiceSaidAboutTheVersion(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	e.sandbox.SeedUpgradeRefusal("9.9.9", "Upload format 0.1.x is retired on 2026-09-01.")
	out := e.statusOutput()

	for _, want := range []string{"9.9.9", "The service says:", "retired on 2026-09-01", "trajector upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
}

// A refusal the service explained is cleared the moment it accepts an
// upload, so its presence outranks any comparison this build can make:
// uploads are stopped now, whatever the arithmetic says.
func TestStatusRelaysALiveRefusalEvenWhenTheMinimumLooksMet(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	e.sandbox.SeedUpgradeRefusal("0.1.0", "This build is blocked; move to 0.2.0.")
	out := e.statusOutput()

	for _, want := range []string{"This build is blocked", "trajector upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
}

func TestStatusRefusesToLetTheServiceForgeItsOwnLines(t *testing.T) {
	// A status report is what a user trusts to tell them the state of
	// their machine. Text the service chose is printed inside it, so a
	// message that can move the cursor or start a line could write any
	// verdict it likes under our name.
	e := newEnv(t)
	e.sandbox.SeedHandshake(proxytest.Handshake{Notice: "hello\rgoodbye"})
	e.sandbox.SeedUpgradeRefusal("9.9.9", "upgrade\n  Capture: off, nothing recorded\x1b[2K")
	out := e.statusOutput()

	if strings.Contains(out, "\x1b") || strings.Contains(out, "\r") {
		t.Errorf("status = %q, want no escapes from the service's text", out)
	}
	if strings.Contains(out, "\n  Capture: off") {
		t.Errorf("status = %q, want the forged line folded back into one", out)
	}
	for _, want := range []string{"upgrade", "Capture: off", "hello goodbye"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want the words themselves kept (%q)", out, want)
		}
	}
}

// writeUploadFile seeds one of the uploader's bookkeeping files, whose
// on-disk shapes are documented formats the dashboard reads.
func writeUploadFile(t *testing.T, e *env, name string, contents map[string]any) {
	t.Helper()
	data, err := json.Marshal(contents)
	if err != nil {
		t.Fatal(err)
	}
	dir := e.layout().UploadDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A pause with an expiry is the one standing whose answer is "wait": a
// user who cannot see when the wait ends has no way to tell a pause
// from a fault.
func TestStatusNamesWhenAPausedUploadResumes(t *testing.T) {
	until := time.Date(2026, 8, 2, 14, 32, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, want string
		reason     proxytest.Reason
	}{
		{name: "the service asked", reason: proxytest.RateLimited, want: "the service asked to slow down"},
		{name: "the attempt ran long", reason: proxytest.TimedOut, want: "the last attempt ran out of time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.sandbox.SeedUploadPause(tc.reason, until)
			out := e.statusOutput()

			if !strings.Contains(out, "Uploads are paused until "+until.Format(time.RFC3339)) {
				t.Errorf("status = %q, want the time the pause ends", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("status = %q, want %q", out, tc.want)
			}
		})
	}
}

// An empty spool beside a full quarantine reads as a healthy idle
// device unless something says otherwise: nothing is waiting to upload
// because everything left is set aside.
func TestStatusSaysWhenTheOnlyRecordsLeftAreQuarantined(t *testing.T) {
	e := newEnv(t)
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-1"}, map[string][]byte{"req-1": []byte("{}")})
	out := e.statusOutput()

	if !strings.Contains(out, "every rawcall left on this machine is quarantined") {
		t.Errorf("status = %q, want the quarantine-only standing", out)
	}
}
