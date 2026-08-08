package lifecycle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func (e *env) statusOutput() string {
	e.t.Helper()
	if err := e.machine().Status(e.project, e.io()); err != nil {
		e.t.Fatalf("status: %v\nstdout: %s", err, e.stdout)
	}
	return e.stdout.String()
}

func TestStatusOnAFreshDevice(t *testing.T) {
	e := newUnpairedEnv(t)
	out := e.statusOutput()

	for _, want := range []string{
		"Not signed in",
		"`trajector login`",
		"Not enabled",
		"`trajector enable`",
		"Not running",
		"0 B of 2.0 GiB used",
		"Never uploaded",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("status on a healthy fresh device warns: %q", out)
	}
}

func TestStatusShowsAnEnabledProjectAndRunningProxy(t *testing.T) {
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
		"Recorded today: 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "third-party") {
		t.Errorf("official upstream labelled third-party: %q", out)
	}
}

func TestStatusLabelsAThirdPartyUpstream(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.environ["ANTHROPIC_BASE_URL"] = "https://relay.example.com"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	e.stdout.Reset()
	out := e.statusOutput()

	if !strings.Contains(out, "https://relay.example.com") || !strings.Contains(out, "third-party") {
		t.Errorf("status = %q, want the third-party upstream shown and labelled", out)
	}
}

func TestStatusExplainsADeviceWidePause(t *testing.T) {
	e := newEnv(t)
	e.sandbox.Pause(routing.PauseSignedOut)
	out := e.statusOutput()

	if !strings.Contains(out, "paused") || !strings.Contains(out, "`trajector login`") {
		t.Errorf("status = %q, want the signed-out pause explained with the command to run", out)
	}
}

func TestStatusExplainsAConsentReconfirmPause(t *testing.T) {
	e := newEnv(t)
	e.sandbox.Pause(routing.PauseConsentReconfirm)
	out := e.statusOutput()

	if !strings.Contains(out, "agreement") || !strings.Contains(out, "`trajector enable`") {
		t.Errorf("status = %q, want the reconfirm pause explained with the command to run", out)
	}
}

func TestStatusShowsAnUnrecognizedPauseReasonVerbatim(t *testing.T) {
	e := newEnv(t)
	// A pause reason this build does not know (say, written by a newer
	// one) must still be shown, not hidden.
	e.sandbox.Pause("some_future_reason")
	out := e.statusOutput()

	if !strings.Contains(out, "some_future_reason") {
		t.Errorf("status = %q, want the unknown pause reason shown verbatim", out)
	}
}

func TestStatusWarnsWhenInjectionAndRoutingDisagree(t *testing.T) {
	e := newEnv(t)
	// A grant with no matching injection: the routing table says this
	// project contributes, the settings say nothing routes here.
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-orphaned-grant",
		ProjectIDHash: e.status().Hash,
		RootPath:      e.canonicalRoot(),
		Upstream:      "https://api.anthropic.com",
	})
	out := e.statusOutput()

	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "`trajector doctor`") {
		t.Errorf("status = %q, want a loud disagreement warning pointing at doctor", out)
	}
}

func TestStatusReportsAForeignPortHolder(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()
	out := e.statusOutput()

	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "not the trajector proxy") {
		t.Errorf("status = %q, want a loud foreign-process warning", out)
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

func TestStatusShowsSpoolUsageAndLastUpload(t *testing.T) {
	e := newEnv(t)
	e.sandbox.SeedRawcall("req-1", "hash-project", e.deps.Now())
	writeUploadFile(t, e, "state.json", map[string]any{
		"last_upload": map[string]any{
			"batch_id": "b-1", "records": 3, "bytes": 2048,
			"at": "2026-08-02T10:00:00Z",
		},
		"last_error":    "boom",
		"last_error_at": "2026-08-02T11:00:00Z",
	})
	out := e.statusOutput()

	if strings.Contains(out, "0 B of") {
		t.Errorf("status = %q, want nonzero spool usage after a seeded rawcall", out)
	}
	for _, want := range []string{"Last upload: 3 rawcall(s)", "2026-08-02T10:00:00Z", "boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
}

func TestStatusWarnsWhenTheSpoolIsFull(t *testing.T) {
	e := newEnv(t)
	writeUploadFile(t, e, "handshake.json", map[string]any{"spool_quota_bytes": 1})
	e.sandbox.SeedRawcall("req-1", "hash-project", e.deps.Now())
	out := e.statusOutput()

	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "full") {
		t.Errorf("status = %q, want a loud spool-full warning", out)
	}
}

func TestStatusWarnsAboutRejectedBatches(t *testing.T) {
	e := newEnv(t)
	batchDir := filepath.Join(e.layout().RejectedDir(), "b-poison")
	if err := os.MkdirAll(batchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"req-1.json", "req-2.json"} {
		if err := os.WriteFile(filepath.Join(batchDir, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reason, _ := json.Marshal(map[string]any{"batch_id": "b-poison", "records": 2, "details": "413 Request Entity Too Large"})
	if err := os.WriteFile(filepath.Join(batchDir, "reason.json"), reason, 0o600); err != nil {
		t.Fatal(err)
	}
	out := e.statusOutput()

	for _, want := range []string{"WARNING", "2 rawcall(s)", "1 rejected batch(es)", "not be retried automatically", "`trajector doctor`"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
}

func TestStatusRendersEverySectionWhenTheSpoolCannotOpen(t *testing.T) {
	e := newEnv(t)
	writeUploadFile(t, e, "handshake.json", map[string]any{"notice": "scheduled maintenance on Friday"})
	e.obstruct(e.layout().SpoolDir())
	out := e.statusOutput()

	if want := "the capture spool at " + e.layout().SpoolDir() + " is not usable"; !strings.Contains(out, want) {
		t.Errorf("status = %q, want the spool section to contain %q", out, want)
	}
	for _, want := range []string{"Uploads", "Never uploaded", "scheduled maintenance on Friday", "`trajector doctor`"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "full") {
		t.Errorf("status = %q, want no spool-full warning for a spool that never opened", out)
	}
}

func TestStatusShowsRejectedBatchesAlongsideASpoolError(t *testing.T) {
	e := newEnv(t)
	seedRejectedBatch(t, e, "b-poison", "413 Request Entity Too Large", map[string][]byte{
		"req-1": spooledEnvelope(t, "req-1", e.deps.Now()),
	})
	e.obstruct(e.layout().SpoolDir())
	out := e.statusOutput()

	for _, want := range []string{"is not usable", "1 rejected batch(es)", "not be retried automatically"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
}

func TestStatusWarnsWhenTheRejectedBatchesCannotBeRead(t *testing.T) {
	e := newEnv(t)
	e.obstruct(e.layout().RejectedDir())
	out := e.statusOutput()

	if want := "the rejected batches at " + e.layout().RejectedDir() + " could not be read"; !strings.Contains(out, want) {
		t.Errorf("status = %q, want it to contain %q", out, want)
	}
	if !strings.Contains(out, "`trajector doctor`") {
		t.Errorf("status = %q, want it to point at doctor", out)
	}
}

func TestStatusShowsServiceMinVersionAndNotice(t *testing.T) {
	e := newEnv(t)
	writeUploadFile(t, e, "handshake.json", map[string]any{
		"min_client_version": "9.9.9",
		"notice":             "scheduled maintenance on Friday",
	})
	out := e.statusOutput()

	for _, want := range []string{"9.9.9", "testv", "scheduled maintenance on Friday"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
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
