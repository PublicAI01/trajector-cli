package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// newUploadEnv is a paired clitest environment whose service
// acknowledges every uploaded batch.
func newUploadEnv(t *testing.T) *clitest.Env {
	t.Helper()
	e := clitest.New(t)
	e.Paired()
	stubEchoAck(t, e.Service())
	return e
}

// TODO: converge the three sibling ack builders (echoAck in upload,
// stubEchoAck here, ackBatch in lifecycle) into fakeplatform the next
// time ack semantics change.
func stubEchoAck(t *testing.T, service *fakeplatform.Server) {
	t.Helper()
	service.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		parts, err := fakeplatform.Parts(r)
		if err != nil {
			return fakeplatform.JSON(590, map[string]any{"error": err.Error()})
		}
		var env struct {
			BatchID string `json:"batch_id"`
		}
		if err := json.Unmarshal(parts["batch"], &env); err != nil || env.BatchID == "" {
			return fakeplatform.JSON(590, map[string]any{"error": "no batch id in envelope"})
		}
		return fakeplatform.JSON(200, map[string]any{"batch_id": env.BatchID})
	})
}

// captured upgrades a seeded rawcall to the shape a real capture
// records — client version, upstream origin, session identity, and the
// upstream's request id — so upload exercises a full envelope.
func captured(id string) proxytest.RawcallOption {
	return func(o *envelope.Observation) {
		o.ClientVersion = "test"
		o.Upstream = "https://api.anthropic.com"
		o.OfficialUpstream = "https://api.anthropic.com"
		o.Request = []byte(`{"model":"m","metadata":{"user_id":"sess"},"messages":[{"role":"user","content":"hello"}]}`)
		o.Response = []byte(`{"id":"` + id + `","type":"message"}`)
		o.UpstreamRequestID = id
	}
}

func seedRawcall(e *clitest.Env, id string, at time.Time) {
	e.Sandbox().SeedRawcall(id, e.ProjectHash(), at, captured(id))
}

func batchUploads(service *fakeplatform.Server) int {
	uploads := 0
	for _, r := range service.Requests() {
		if strings.HasPrefix(r.URL, "/v1/batches") {
			uploads++
		}
	}
	return uploads
}

func TestUploadForceDrainsTheSpoolThroughTheProxy(t *testing.T) {
	e := newUploadEnv(t)
	seedRawcall(e, "req-1", time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Uploaded 1 batch(es), 1 rawcall(s).") {
		t.Errorf("stdout = %q", got.Stdout)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 0 {
		t.Errorf("spool holds %d rawcalls after an acknowledged upload", n)
	}
	if n := batchUploads(e.Service()); n != 1 {
		t.Errorf("service saw %d uploads, want 1", n)
	}
}

func TestUploadPrintsTheAuthenticationRemedyForAnUnverifiedProxy(t *testing.T) {
	e := newUploadEnv(t)
	im := proxytest.StartImposter(t, proxytest.Health{Service: apiproxy.ServiceName, Version: "dev"})
	im.ReplayProof("00000000000000000000000000000000")
	proxytest.PublishAdminToken(t, e.Layout(), im.Addr(), "feedfacefeedfacefeedfacefeedface")
	t.Setenv(cli.ProxyAddrEnv, im.Addr())

	got := e.Run("upload", "--force")
	if got.Exit != 1 {
		t.Fatalf("exit = %d (stderr: %q), want a loud failure", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "could not verify the proxy") || !strings.Contains(got.Stderr, "authentication problem") {
		t.Errorf("stderr = %q, want the authentication reading offered", got.Stderr)
	}
	if strings.Contains(got.Stderr, "find and stop the process") {
		t.Errorf("stderr = %q, must not advise hunting a process that may be our own proxy", got.Stderr)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

func TestUploadRefusesAHealthzCopyingPortHolder(t *testing.T) {
	e := newUploadEnv(t)
	seedRawcall(e, "req-1", time.Now().UTC())
	im := proxytest.StartImposter(t, proxytest.Health{Service: apiproxy.ServiceName, Version: "dev"})
	proxytest.PublishAdminToken(t, e.Layout(), im.Addr(), "feedfacefeedfacefeedfacefeedface")
	t.Setenv(cli.ProxyAddrEnv, im.Addr())

	got := e.Run("upload", "--force")
	if got.Exit != 1 {
		t.Fatalf("exit = %d (stderr: %q), want a loud refusal", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "not the trajector proxy") {
		t.Errorf("stderr = %q", got.Stderr)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the captured data kept", n)
	}
}

func TestUploadWithoutForceRespectsThresholds(t *testing.T) {
	e := newUploadEnv(t)
	seedRawcall(e, "req-1", time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Below the upload thresholds") {
		t.Errorf("stdout = %q", got.Stdout)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the record kept", n)
	}
}

func TestUploadWhileSignedOutPausesAndKeepsData(t *testing.T) {
	e := clitest.New(t)
	stubEchoAck(t, e.Service())
	seedRawcall(e, "req-1", time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "trajector login") {
		t.Errorf("stdout = %q", got.Stdout)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the record kept", n)
	}
}

func TestIdleExitRunsAFinalFlush(t *testing.T) {
	e := newUploadEnv(t)
	seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy("--idle-timeout", "150ms")

	select {
	case <-p.Stopped():
	case <-time.After(10 * time.Second):
		t.Fatal("proxy did not idle out")
	}
	if n := len(e.Sandbox().Rawcalls()); n != 0 {
		t.Errorf("spool holds %d rawcalls after the final flush", n)
	}
	if n := batchUploads(e.Service()); n != 1 {
		t.Errorf("service saw %d uploads, want the final flush", n)
	}
}

func TestUploadSurfacesEveryRejectedBatchLoudly(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.JSON(400, map[string]any{"error": "bad multipart"}))
	seedRawcall(e, "req-1", time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 1 {
		t.Fatalf("exit = %d (stdout: %q)", got.Exit, got.Stdout)
	}
	if !strings.Contains(got.Stderr, "rejected") || !strings.Contains(got.Stderr, "moved to") {
		t.Errorf("stderr = %q, want a loud rejection notice naming the rejected store", got.Stderr)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 0 {
		t.Errorf("spool still holds %d rawcalls; the rejected batch must move aside", n)
	}
	if n := rejectedRecords(t, e.Layout().RejectedDir()); n != 1 {
		t.Errorf("rejected store holds %d records, want 1", n)
	}

	seedRawcall(e, "req-2", time.Now().UTC())
	again := e.Run("upload", "--force")
	if again.Exit != 1 {
		t.Fatalf("second rejection exit = %d (stdout: %q)", again.Exit, again.Stdout)
	}
	if n := rejectedRecords(t, e.Layout().RejectedDir()); n != 2 {
		t.Errorf("rejected store holds %d records after two rejections, want 2", n)
	}
}

func TestUploadSetsAsideATornRawcallAndKeepsUploading(t *testing.T) {
	e := newUploadEnv(t)
	seedRawcall(e, "req-good", time.Now().UTC())
	e.Sandbox().SeedTornRawcall("req-torn", e.ProjectHash(), time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	for _, want := range []string{
		"Uploaded 1 batch(es), 1 rawcall(s).",
		"Set aside 1 unreadable rawcall(s)",
	} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", got.Stdout, want)
		}
	}
	if n := len(e.Sandbox().Rawcalls()); n != 0 {
		t.Errorf("spool holds %d rawcalls; the torn record must move aside, the rest upload", n)
	}
	if n := rejectedRecords(t, e.Layout().RejectedDir()); n != 1 {
		t.Errorf("rejected store holds %d records, want the torn one", n)
	}
	if n := batchUploads(e.Service()); n != 1 {
		t.Errorf("service saw %d uploads, want 1", n)
	}

	status := e.InProject("status")
	if !strings.Contains(status.Stdout, "1 rawcall(s) in 1 rejected batch(es)") {
		t.Errorf("status stdout = %q, want the quarantined count", status.Stdout)
	}
	doc := e.InProject("doctor")
	if doc.Exit != 1 {
		t.Fatalf("doctor exit = %d, want 1 while a record waits quarantined (stdout: %q)", doc.Exit, doc.Stdout)
	}
	if !strings.Contains(doc.Stdout, "unreadable in the spool") {
		t.Errorf("doctor stdout = %q, want the recorded set-aside reason", doc.Stdout)
	}
}

func TestUploadPausedByARequiredUpgradeExitsZeroEveryTime(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}))
	seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy()
	defer p.Stop()

	runs := []struct {
		name string
		args []string
	}{
		{"first encounter", []string{"upload"}},
		{"repeat", []string{"upload"}},
		{"forced", []string{"upload", "--force"}},
	}
	for _, run := range runs {
		got := e.Run(run.args...)
		if got.Exit != 0 {
			t.Fatalf("%s: exit = %d (stderr: %q)", run.name, got.Exit, got.Stderr)
		}
		if !strings.Contains(got.Stdout, "Uploads are paused") || !strings.Contains(got.Stdout, "Captured data is kept") {
			t.Errorf("%s: stdout = %q", run.name, got.Stdout)
		}
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the record kept", n)
	}
	if n := batchUploads(e.Service()); n != 2 {
		t.Errorf("service saw %d upload attempts, want the first encounter and the forced retry only", n)
	}
}

func TestUploadDeferredByTheServiceExitsZeroEveryTime(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	limited := fakeplatform.JSON(429, map[string]any{})
	limited.Header.Set("Retry-After", "3600")
	e.Service().Stub("POST", "/v1/batches", limited)
	seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy()
	defer p.Stop()

	for _, name := range []string{"first encounter", "repeat"} {
		got := e.Run("upload")
		if got.Exit != 0 {
			t.Fatalf("%s: exit = %d (stderr: %q)", name, got.Exit, got.Stderr)
		}
		if !strings.Contains(got.Stdout, "asked to slow down") {
			t.Errorf("%s: stdout = %q", name, got.Stdout)
		}
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the record kept", n)
	}
}

func TestUploadReportsSetAsideRecordsEvenWhenTheServicePausesUploads(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}))
	seedRawcall(e, "req-good", time.Now().UTC().Add(-25*time.Hour))
	e.Sandbox().SeedTornRawcall("req-torn", e.ProjectHash(), time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	for _, want := range []string{
		"Set aside 1 unreadable rawcall(s)",
		"Uploads are paused",
	} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", got.Stdout, want)
		}
	}
	if n := rejectedRecords(t, e.Layout().RejectedDir()); n != 1 {
		t.Errorf("rejected store holds %d records, want the torn one", n)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the good record kept through the pause", n)
	}
}

func TestUploadWaitsOutADeferralThatNamesNoPause(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.JSON(429, map[string]any{}))
	seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy()
	defer p.Stop()

	for _, name := range []string{"first encounter", "during the default pause"} {
		got := e.Run("upload")
		if got.Exit != 0 {
			t.Fatalf("%s: exit = %d (stderr: %q)", name, got.Exit, got.Stderr)
		}
		if !strings.Contains(got.Stdout, "asked to slow down") {
			t.Errorf("%s: stdout = %q", name, got.Stdout)
		}
	}
	if n := batchUploads(e.Service()); n != 1 {
		t.Errorf("service saw %d upload attempts, want the default pause to hold off the second", n)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the record kept", n)
	}
}

func TestUploadUsage(t *testing.T) {
	e := clitest.New(t)
	if got := e.Run("upload", "--frobnicate"); got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector upload") {
		t.Errorf("bad flag = %+v", got)
	}
}

// rejectedRecords sums the record counts ListRejected reports.
func rejectedRecords(t *testing.T, dir string) int {
	t.Helper()
	batches, err := upload.ListRejected(dir)
	if err != nil {
		t.Fatalf("ListRejected: %v", err)
	}
	n := 0
	for _, b := range batches {
		n += b.Records
	}
	return n
}

func TestARejectedBatchCannotWriteItsOwnLineIntoDoctorsVerdicts(t *testing.T) {
	// The service's rejection wording is printed by upload, and then by
	// doctor in the middle of a run of ok:/fixed:/problem: lines the
	// user reads as trajector's own conclusions. A rejection body that
	// carries a carriage return, an erase-line escape, or a newline
	// could forge one of those lines outright.
	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.Response{
		Status: 400,
		Body:   []byte("bad multipart\r\x1b[2K  ok: everything checks out\n  ok: nothing to do"),
	})
	seedRawcall(e, "req-1", time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 1 {
		t.Fatalf("exit = %d (stdout: %q)", got.Exit, got.Stdout)
	}
	doc := e.InProject("doctor")
	if doc.Exit != 1 {
		t.Fatalf("doctor exit = %d, want 1 while a batch waits quarantined (stdout: %q)", doc.Exit, doc.Stdout)
	}
	for _, out := range []struct {
		what string
		text string
	}{{"upload stderr", got.Stderr}, {"doctor stdout", doc.Stdout}} {
		if strings.Contains(out.text, "\x1b") || strings.Contains(out.text, "\r") {
			t.Errorf("%s = %q, want no rune the service supplied that can move the cursor", out.what, out.text)
		}
		for _, line := range strings.Split(out.text, "\n") {
			if strings.TrimSpace(line) == "ok: everything checks out" {
				t.Errorf("%s = %q, want the forged verdict to have stayed inside the quoted detail", out.what, out.text)
			}
		}
		if !strings.Contains(out.text, "bad multipart") {
			t.Errorf("%s = %q, want what the service said still readable", out.what, out.text)
		}
	}
}
