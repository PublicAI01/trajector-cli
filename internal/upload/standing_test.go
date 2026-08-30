package upload_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func rateLimited(retryAfter string) fakeplatform.Response {
	limited := fakeplatform.JSON(429, map[string]any{})
	limited.Header.Set("Retry-After", retryAfter)
	return limited
}

func TestAPauseTheServiceAskedForOutlivesTheProcessThatWasToldIt(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rateLimited("3600"))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now.Add(-25*time.Hour))

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("429 did not surface")
	}

	restarted := f.newUploader(t)
	res, err := restarted.Flush(false)
	if err != nil || res.Outcome != upload.Deferred {
		t.Fatalf("automatic flush after a restart = %+v, %v, want the pause still honoured", res, err)
	}
	if res.Standing.Reason != upload.RateLimited {
		t.Errorf("standing = %+v, want the service's own request named", res.Standing)
	}
	if f.uploadCount() != 1 {
		t.Errorf("the service saw %d attempts, want the restarted flusher to hold off", f.uploadCount())
	}
}

func TestAPauseEndsWhenItsTimeRunsOut(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rateLimited("120"))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now.Add(-25*time.Hour))

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("429 did not surface")
	}
	f.now = f.now.Add(121 * time.Second)
	if got := f.standing(upload.RateLimited); got.Held() {
		t.Errorf("standing after the pause elapsed = %+v, want none", got)
	}
	res, err := f.newUploader(t).Flush(false)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("flush after the pause = %+v, %v", res, err)
	}
}

func TestAnAcknowledgementEndsThePauseThatPrecededIt(t *testing.T) {
	// The service's most recent word wins: a batch it has just taken is
	// newer evidence than the wait it asked for earlier, and holding
	// uploads back on the older word would keep back exactly what the
	// service has shown it will accept.
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rateLimited("3600"))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now.Add(-25*time.Hour))

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("429 did not surface")
	}
	if got := f.standing(upload.RateLimited); !got.Held() {
		t.Fatal("the pause was not recorded")
	}
	if res, err := f.uploader.Flush(true); err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("forced flush past the pause = %+v, %v", res, err)
	}
	if got := f.standing(upload.RateLimited); got.Held() {
		t.Errorf("the pause = %+v, want it cleared by the acknowledgement that answered it", got)
	}
	if res, err := f.newUploader(t).Flush(false); err != nil || res.Outcome != upload.Empty {
		t.Fatalf("automatic flush after the acknowledgement = %+v, %v", res, err)
	}
}

func TestATimedOutAttemptIsNotTheServiceAskingToSlowDown(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", timeoutStub())
	f.storeRawcall(t, "req-1", f.now.Add(-25*time.Hour))

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("the timed-out attempt did not surface")
	}
	got := f.standing(upload.TimedOut)
	if !got.Held() {
		t.Fatalf("standings on disk = %v, want the timed-out attempt recorded", got)
	}
	if !strings.Contains(got.Explain(), "ran out of time") {
		t.Errorf("explanation = %q, want the attempt's own failure named, not the service's request", got.Explain())
	}
	if f.standing(upload.RateLimited).Held() {
		t.Error("a timed-out attempt was recorded as the service asking to slow down")
	}
}

func TestEveryStandingNamesWhatIsTrueAndTheGatesNameWhatEndsThem(t *testing.T) {
	// A reason that explains nothing is a reason a surface has to word
	// for itself, which is how three surfaces come to word one condition
	// three ways.
	at := time.Date(2026, 8, 30, 14, 32, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		standing upload.Standing
		explain  string
		remedy   string
	}{
		{
			name:     "signed out",
			standing: upload.Standing{Reason: upload.SignedOut},
			explain:  "Uploads are paused: this device is signed out.",
			remedy:   "Run `trajector login` to pair this device; uploads resume then.",
		},
		{
			name:     "rate limited",
			standing: upload.Standing{Reason: upload.RateLimited, NotBefore: at},
			explain:  "Uploads are paused until 2026-08-30T14:32:00Z: the service asked to slow down.",
			remedy:   "Uploads resume automatically; `trajector upload --force` offers them now.",
		},
		{
			name:     "timed out",
			standing: upload.Standing{Reason: upload.TimedOut, NotBefore: at},
			explain:  "Uploads are paused until 2026-08-30T14:32:00Z: the last attempt ran out of time.",
			remedy:   "Uploads resume automatically; `trajector upload --force` offers them now.",
		},
		{
			name:     "quarantine only",
			standing: upload.Standing{Reason: upload.QuarantineOnly},
			explain:  "Uploads have nothing to send: every rawcall left on this machine is quarantined.",
			remedy:   "",
		},
		{
			name:     "a refusal that named no minimum",
			standing: upload.Standing{Reason: upload.VersionGate, Refused: true, Upgradable: true},
			explain:  "Uploads are paused: the service refuses this build's version.",
			remedy:   "Run `trajector upgrade` to install the newest release.",
		},
		{
			name:     "a minimum no order covers",
			standing: upload.Standing{Reason: upload.VersionGate, MinClientVersion: "9.9.9", Version: "dev"},
			explain:  "The service requires client version 9.9.9 or newer; this build is dev.",
			remedy:   "",
		},
		{
			name:     "an authorization refusal that named no address",
			standing: upload.Standing{Reason: upload.AuthorizationGate},
			explain:  "Uploads are paused: this account's data authorization is not complete.",
			remedy:   "Complete your data authorization in the Trajector dashboard, then uploads resume.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.standing.Explain(); got != tc.explain {
				t.Errorf("Explain() = %q, want %q", got, tc.explain)
			}
			if got := tc.standing.Remedy(); got != tc.remedy {
				t.Errorf("Remedy() = %q, want %q", got, tc.remedy)
			}
		})
	}
}

func TestAMinimumThisBuildMeetsIsNotAReasonToStop(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, map[string]any{"min_client_version": "0.9.0"}))
	f.storeRawcall(t, "req-1", f.now.Add(-25*time.Hour))

	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	if held := upload.LoadStandings(f.dir, "1.0.0", f.now); len(held) != 0 {
		t.Errorf("standings = %v, want none for a build that meets the stated minimum", held)
	}
	if held := upload.LoadStandings(f.dir, "0.1.0", f.now); len(held) != 1 || held[0].Reason != upload.VersionGate {
		t.Errorf("standings for an older build = %v, want the version gate", held)
	}
}
