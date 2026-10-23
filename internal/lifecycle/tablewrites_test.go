package lifecycle_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// routeToFakeUpstream makes the fake upstream the base URL every
// project enabled from here on is granted, so exchanges through the
// live proxy reach it and can be recorded.
func (e *env) routeToFakeUpstream() {
	e.environ["ANTHROPIC_BASE_URL"] = e.proxyEnv.Upstream.URL()
}

// tokenOf is the token of the standing grant for the project at dir.
func (e *env) tokenOf(dir string) string {
	e.t.Helper()
	g, ok := e.sandbox.ActiveGrant(dir)
	if !ok {
		e.t.Fatalf("no standing grant for %s", dir)
	}
	return g.Token
}

// exchange sends one message request carrying token through the live
// proxy, as a session the token was injected into sends it, and reads
// the reply. The upstream answers with the response id given.
func (e *env) exchange(token, responseID string) {
	e.t.Helper()
	e.proxyEnv.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"` + responseID + `"}`)})
	if reply := e.proxyEnv.Post("/t/"+token+"/v1/messages", `{"m":1}`, nil); reply.StatusCode != http.StatusOK {
		e.t.Fatalf("exchange %s: status %d, want it forwarded", responseID, reply.StatusCode)
	}
}

// spooledIDs lists the response ids of the rawcalls on disk.
func (e *env) spooledIDs() []string {
	e.t.Helper()
	var ids []string
	for _, rc := range e.sandbox.Rawcalls() {
		ids = append(ids, rc.RequestID)
	}
	return ids
}

// waitSpooled waits for the rawcall with responseID to be on disk.
// Captures are written one at a time in the order exchanges end, so once
// it is there every exchange that ended before it has had its turn.
func (e *env) waitSpooled(responseID string) []string {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ids := e.spooledIDs()
		if slices.Contains(ids, responseID) {
			return ids
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("spool holds %q, want %s", ids, responseID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAnExchangeRightAfterTheSessionHookPausesRecordingIsNotRecorded(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.routeToFakeUpstream()
	if err := e.machine().Enable(t.Context(), e.project, choices(proxytest.WithProxy), e.io()); err != nil {
		t.Fatal(err)
	}
	token := e.tokenOf(e.project)
	e.exchange(token, "msg_before")
	e.waitSpooled("msg_before")

	e.sandbox.AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z")
	if err := e.machine().EnsureProxy(t.Context(), e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if reason := e.sandbox.PausedReason(); reason != proxytest.PauseConsentReconfirm {
		t.Fatalf("pause = %q after the session hook, want the reconfirmation pause", reason)
	}
	e.exchange(token, "msg_paused")

	e.sandbox.Resume(proxytest.PauseConsentReconfirm)
	e.exchange(token, "msg_resumed")
	if ids := e.waitSpooled("msg_resumed"); slices.Contains(ids, "msg_paused") {
		t.Errorf("spool holds %q, want nothing from the exchange that started after the pause was stored", ids)
	}
}

func TestAnExchangeRightAfterDisableLeavesNothingInTheSpool(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.routeToFakeUpstream()
	if err := e.machine().Enable(t.Context(), e.project, choices(proxytest.WithProxy), e.io()); err != nil {
		t.Fatal(err)
	}
	other := e.enableAt(t.TempDir())
	withdrawn := e.tokenOf(e.project)
	e.exchange(withdrawn, "msg_before")
	e.waitSpooled("msg_before")

	if err := e.machine().Disable(t.Context(), e.project, false, e.io()); err != nil {
		t.Fatal(err)
	}
	if ids := e.spooledIDs(); len(ids) != 0 {
		t.Fatalf("spool holds %q after disable, want it emptied", ids)
	}
	// A session that started before disable keeps sending the token it
	// was injected with.
	e.exchange(withdrawn, "msg_after_disable")

	e.exchange(e.tokenOf(other), "msg_other")
	if ids := e.waitSpooled("msg_other"); !slices.Equal(ids, []string{"msg_other"}) {
		t.Errorf("spool holds %q, want only the still enabled project's exchange", ids)
	}
}

func TestEnableRightAfterAnotherProjectsEnablePassesItsSelfcheck(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	// The first enable's self-check makes the proxy read the table that
	// holds only the first grant.
	e.enableAt(t.TempDir())

	second := t.TempDir()
	if err := e.machine().Enable(t.Context(), second, choices(proxytest.WithProxy), e.io()); err != nil {
		t.Fatalf("enable right after another project's enable = %v, want its self-check to know the new token", err)
	}
	if reply := e.proxyEnv.Selfcheck(e.tokenOf(second)); !reply.TokenKnown || !reply.Recording {
		t.Errorf("selfcheck = %+v, want the new project routed and recorded", reply)
	}
}
