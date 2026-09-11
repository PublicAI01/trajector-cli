package routing_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func openStore(t *testing.T) (*routing.Store, *routing.Table) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	return routing.OpenStore(path), routing.New(path, time.Nanosecond)
}

func grant(t *testing.T, s *routing.Store, token, root string) {
	t.Helper()
	err := s.Grant(routing.Grant{
		Token:         token,
		ProjectIDHash: "hash-" + token,
		RootPath:      root,
		Upstream:      "https://api.anthropic.com",
		GrantedAt:     "2026-08-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGrantMakesTokenResolvable(t *testing.T) {
	store, table := openStore(t)
	grant(t, store, "tok-1", "/home/dev/project")

	route, verdict := table.Lookup("tok-1")
	if !verdict.Records() {
		t.Fatalf("fresh grant verdict = %+v, want Record", verdict)
	}
	if route.ProjectIDHash != "hash-tok-1" {
		t.Errorf("route = %+v", route)
	}
	stored, ok, err := store.Active("/home/dev/project")
	if err != nil || !ok || stored.Token != "tok-1" || stored.RootPath != "/home/dev/project" {
		t.Errorf("Active = %+v, %v, %v", stored, ok, err)
	}
}

func TestGrantReadsBackTheShapeItWasGrantedIn(t *testing.T) {
	store, _ := openStore(t)
	if err := store.Grant(routing.Grant{
		Token:         "tok-files",
		ProjectIDHash: "hash-files",
		RootPath:      "/home/dev/files",
		Upstream:      "https://api.anthropic.com",
		GrantedAt:     "2026-08-01T00:00:00Z",
		Shape:         routing.WithoutProxy,
	}); err != nil {
		t.Fatal(err)
	}
	grant(t, store, "tok-proxy", "/home/dev/proxy")

	shapes := map[string]routing.Shape{}
	grants, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		shapes[g.Token] = g.Shape
	}
	if shapes["tok-files"] != routing.WithoutProxy {
		t.Errorf("shape of a grant made without the proxy = %q, want %q", shapes["tok-files"], routing.WithoutProxy)
	}
	if shapes["tok-proxy"] != routing.WithProxy {
		t.Errorf("shape of a grant made with the proxy = %q, want %q", shapes["tok-proxy"], routing.WithProxy)
	}
}

// TestGrantRetiresThePreviousTokenInsteadOfDeletingIt pins the
// 2026-09-18 fix. A re-enable rotates the token, but the token it
// rotates away from is still exported into every Claude Code session
// that started before the rotation — a session reads the injected base
// URL once and carries it for the rest of its life. Deleting the old
// entry made those requests resolve to nothing, and the data path
// answers an unknown token with the default upstream: a project chained
// to a third-party relay had its own relay credentials carried to the
// official endpoint the moment a disable/enable pair ran underneath a
// live session. That is the guarantee Revoke states in so many words —
// a route that stops recording keeps forwarding where it was recorded
// to go — and Grant was the one path that broke it.
func TestGrantRetiresThePreviousTokenInsteadOfDeletingIt(t *testing.T) {
	const (
		root  = "/home/dev/relay-project"
		relay = "https://relay.example.com"
	)
	store, table := openStore(t)
	grantTo := func(token, upstream, at string) {
		t.Helper()
		if err := store.Grant(routing.Grant{
			Token: token, ProjectIDHash: "hash-r", RootPath: root,
			Upstream: upstream, GrantedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	grantTo("tok-old", relay, "2026-08-01T00:00:00Z")
	if err := store.Revoke(root, "2026-08-01T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	grantTo("tok-new", relay, "2026-08-01T02:00:00Z")

	route, verdict := table.Lookup("tok-old")
	if verdict.Decision != routing.ForwardOnlyRevoked {
		t.Fatalf("retired token verdict = %+v, want ForwardOnlyRevoked; a session still holding it now forwards at the default upstream", verdict)
	}
	if route.Upstream != relay {
		t.Errorf("retired token routes at %q, want the recorded third-party upstream %q", route.Upstream, relay)
	}
	if _, verdict := table.Lookup("tok-new"); !verdict.Records() {
		t.Errorf("new token verdict = %+v, want Record", verdict)
	}
	// Exactly one entry stands: rotation must not leave two live tokens.
	active, ok, err := store.Active(root)
	if err != nil || !ok || active.Token != "tok-new" {
		t.Errorf("Active = %+v, %v, %v; want the freshly granted token", active, ok, err)
	}
}

// TestGrantRetiresAStandingEntryOfTheSameRoot is the other half: a
// rotation that happens without an intervening disable — doctor's
// repair, or a re-enable that re-keys — must also leave the displaced
// token forwarding rather than gone.
func TestGrantRetiresAStandingEntryOfTheSameRoot(t *testing.T) {
	store, table := openStore(t)
	grant(t, store, "tok-old", "/home/dev/project")
	grant(t, store, "tok-new", "/home/dev/project")

	if _, verdict := table.Lookup("tok-old"); verdict.Decision != routing.ForwardOnlyRevoked {
		t.Errorf("displaced token verdict = %+v, want ForwardOnlyRevoked", verdict)
	}
	if _, verdict := table.Lookup("tok-new"); !verdict.Records() {
		t.Errorf("new token verdict = %+v, want Record", verdict)
	}
}

func TestRevokeKeepsForwardingUpstream(t *testing.T) {
	store, table := openStore(t)
	if err := store.Grant(routing.Grant{
		Token:         "tok-relay",
		ProjectIDHash: "hash-r",
		RootPath:      "/home/dev/relay-project",
		Upstream:      "https://relay.example.com",
		GrantedAt:     "2026-08-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke("/home/dev/relay-project", "2026-08-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	route, verdict := table.Lookup("tok-relay")
	if verdict.Decision != routing.ForwardOnlyRevoked {
		t.Fatalf("verdict = %+v, want ForwardOnlyRevoked", verdict)
	}
	if route.Upstream != "https://relay.example.com" {
		t.Errorf("upstream = %q, want the recorded third-party upstream", route.Upstream)
	}

	if _, ok, err := store.Active("/home/dev/relay-project"); err != nil || ok {
		t.Errorf("Active after revoke = %v, %v; want none", ok, err)
	}
}

func TestPauseSuspendsEveryRouteAndMatchingResumeLifts(t *testing.T) {
	store, table := openStore(t)
	grant(t, store, "tok-a", "/home/dev/a")
	grant(t, store, "tok-b", "/home/dev/b")

	if err := store.Pause("signed_out"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"tok-a", "tok-b"} {
		if _, verdict := table.Lookup(tok); verdict.Decision != routing.ForwardOnlyPaused || verdict.PauseReason != "signed_out" {
			t.Errorf("%s during pause: verdict = %+v, want ForwardOnlyPaused with the reason", tok, verdict)
		}
	}
	if reason, err := store.PausedReason(); err != nil || reason != "signed_out" {
		t.Errorf("PausedReason = %q, %v", reason, err)
	}

	if err := store.Resume("consent"); err != nil {
		t.Fatal(err)
	}
	if _, verdict := table.Lookup("tok-a"); verdict.Records() {
		t.Error("resume with a different reason lifted the pause")
	}

	if err := store.Resume("signed_out"); err != nil {
		t.Fatal(err)
	}
	if _, verdict := table.Lookup("tok-a"); !verdict.Records() {
		t.Errorf("after matching resume: verdict = %+v, want Record", verdict)
	}
	if reason, _ := store.PausedReason(); reason != "" {
		t.Errorf("PausedReason after resume = %q, want empty", reason)
	}
}

func TestResolveAnswersWhatTheProxyWouldAnswer(t *testing.T) {
	store, table := openStore(t)
	grant(t, store, "tok-active", "/home/dev/a")
	grant(t, store, "tok-revoked", "/home/dev/b")
	if err := store.Revoke("/home/dev/b", "2026-08-01T01:00:00Z"); err != nil {
		t.Fatal(err)
	}

	for _, pause := range []routing.PauseReason{"", "signed_out"} {
		if pause == "" {
			if err := store.Resume("signed_out"); err != nil {
				t.Fatal(err)
			}
		} else if err := store.Pause(pause); err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{"tok-active", "tok-revoked", "tok-unknown"} {
			_, want := table.Lookup(token)
			got, err := store.Resolve(token)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("Resolve(%q) under pause %q = %+v, want the table's %+v", token, pause, got, want)
			}
		}
	}
}

func TestResumeOtherBuildLiftsOnlyWhatAnotherBuildPaused(t *testing.T) {
	tests := []struct {
		name        string
		pausedBy    string
		running     string
		wantResumed bool
		wantNamed   string
	}{
		{name: "another build", pausedBy: "0.0.9", running: "1.0.0", wantResumed: true, wantNamed: "version 0.0.9"},
		{name: "no build recorded", pausedBy: "", running: "1.0.0", wantResumed: true, wantNamed: "an earlier build"},
		{name: "the build that paused", pausedBy: "1.0.0", running: "1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, table := openStore(t)
			grant(t, store, "tok", "/home/dev/p")
			if err := store.PauseByBuild(routing.PauseRedactionDrift, tt.pausedBy); err != nil {
				t.Fatal(err)
			}

			resumed, pausedBy, err := store.ResumeOtherBuild(routing.PauseRedactionDrift, tt.running)
			if err != nil {
				t.Fatal(err)
			}
			if resumed != tt.wantResumed || pausedBy != tt.wantNamed {
				t.Errorf("ResumeOtherBuild = %v, %q; want %v, %q", resumed, pausedBy, tt.wantResumed, tt.wantNamed)
			}
			if _, verdict := table.Lookup("tok"); verdict.Records() != tt.wantResumed {
				t.Errorf("verdict after the lift = %+v, want recording = %v", verdict, tt.wantResumed)
			}
		})
	}
}

func TestResumeOtherBuildLeavesAPauseOfAnotherReasonStanding(t *testing.T) {
	store, _ := openStore(t)
	grant(t, store, "tok", "/home/dev/p")
	if err := store.Pause(routing.PauseSignedOut); err != nil {
		t.Fatal(err)
	}

	resumed, _, err := store.ResumeOtherBuild(routing.PauseRedactionDrift, "1.0.0")
	if err != nil || resumed {
		t.Fatalf("ResumeOtherBuild = %v, %v; want nothing lifted", resumed, err)
	}
	if reason, _ := store.PausedReason(); reason != routing.PauseSignedOut {
		t.Errorf("PausedReason = %q, want the signed-out pause left standing", reason)
	}
}

func TestResumeOtherBuildWritesNoTableWhereThereIsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "routes-under-test.json")

	resumed, _, err := routing.OpenStore(path).ResumeOtherBuild(routing.PauseRedactionDrift, "1.0.0")
	if err != nil || resumed {
		t.Fatalf("ResumeOtherBuild on a missing table = %v, %v; want nothing lifted", resumed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat after the lift = %v, want no table written", err)
	}
}

func TestPauseRequiresReason(t *testing.T) {
	store, _ := openStore(t)
	if err := store.Pause(""); err == nil {
		t.Error("empty pause reason accepted")
	}
}

func TestPauseDoesNotOutliveDedicatedResume(t *testing.T) {
	store, _ := openStore(t)
	grant(t, store, "tok", "/home/dev/p")
	if err := store.Pause("consent"); err != nil {
		t.Fatal(err)
	}
	if err := store.Pause("signed_out"); err != nil {
		t.Fatal(err)
	}
	if err := store.Resume("signed_out"); err != nil {
		t.Fatal(err)
	}
	if reason, _ := store.PausedReason(); reason != "" {
		t.Errorf("PausedReason = %q, want empty after resuming the current reason", reason)
	}
}

func TestSetUpstreamUpdatesOnlyActiveEntry(t *testing.T) {
	store, table := openStore(t)
	grant(t, store, "tok-old", "/home/dev/p")
	if err := store.Revoke("/home/dev/p", "2026-08-01T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	grant(t, store, "tok-live", "/home/dev/p")

	if err := store.SetUpstream("/home/dev/p", "https://relay.example.com", "2026-08-01T02:00:00Z"); err != nil {
		t.Fatal(err)
	}
	route, verdict := table.Lookup("tok-live")
	if !verdict.Records() || route.Upstream != "https://relay.example.com" {
		t.Errorf("active route = %+v, verdict = %+v", route, verdict)
	}
}

func TestSetUpstreamRecordsTheMoveUntilTheNextGrant(t *testing.T) {
	store, _ := openStore(t)
	grant(t, store, "tok-1", "/home/dev/p")

	if err := store.SetUpstream("/home/dev/p", "https://relay.example.com", "2026-08-01T02:00:00Z"); err != nil {
		t.Fatal(err)
	}
	moved, ok, err := store.Active("/home/dev/p")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if moved.UpstreamMoved.From != "https://api.anthropic.com" || moved.UpstreamMoved.At != "2026-08-01T02:00:00Z" {
		t.Errorf("moved = %+v, want the previous upstream and the move time recorded", moved)
	}

	grant(t, store, "tok-2", "/home/dev/p")
	fresh, ok, err := store.Active("/home/dev/p")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if fresh.UpstreamMoved.Happened() {
		t.Errorf("fresh = %+v, want a new grant to reset the move record", fresh)
	}
}

func TestTablesWithoutMoveRecordsStillParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	table := `{"projects":{"tok-1":{"project_id_hash":"hash-1","root_path":"/home/dev/p","upstream":"https://api.anthropic.com","granted_at":"2026-08-01T00:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}

	g, ok, err := routing.OpenStore(path).Active("/home/dev/p")
	if err != nil || !ok {
		t.Fatalf("Active = %v, %v", ok, err)
	}
	if g.UpstreamMoved.Happened() {
		t.Errorf("grant = %+v, want no move recorded on a table written before the field existed", g)
	}
}

func TestStoreStartsFromMissingFile(t *testing.T) {
	store := routing.OpenStore(filepath.Join(t.TempDir(), "missing", "routes-under-test.json"))
	if reason, err := store.PausedReason(); err != nil || reason != "" {
		t.Errorf("PausedReason on missing file = %q, %v", reason, err)
	}
	if all, err := store.All(); err != nil || len(all) != 0 {
		t.Errorf("All on missing file = %v, %v", all, err)
	}
	grant(t, store, "tok", "/home/dev/p")
	if _, ok, err := store.Active("/home/dev/p"); err != nil || !ok {
		t.Errorf("Active after first grant = %v, %v", ok, err)
	}
}

func TestConcurrentGrantsAllSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			suffix := fmt.Sprintf("%02d", i)
			errs[i] = routing.OpenStore(path).Grant(routing.Grant{
				Token:         "tok-" + suffix,
				ProjectIDHash: "hash-" + suffix,
				RootPath:      "/project/" + suffix,
				Upstream:      "https://api.anthropic.com",
				GrantedAt:     "2026-08-01T00:00:00Z",
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}
	grants, err := routing.OpenStore(path).All()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != n {
		t.Errorf("%d grants survived %d concurrent enables, want all of them", len(grants), n)
	}
}

func TestRestoreGrantsLeavesAConcurrentGrantAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	ours := routing.OpenStore(path)

	// Snapshot taken while the table does not exist yet.
	snap, err := ours.SnapshotGrants("/project/ours")
	if err != nil {
		t.Fatal(err)
	}

	// A concurrent enable creates the table with another project's grant.
	grant(t, routing.OpenStore(path), "tok-other", "/project/other")

	// Our enable grants, fails, and rolls back.
	grant(t, ours, "tok-ours", "/project/ours")
	if err := ours.RestoreGrants(snap); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := ours.Active("/project/ours"); err != nil || ok {
		t.Errorf("our grant survived its own rollback: %v, %v", ok, err)
	}
	if _, ok, err := ours.Active("/project/other"); err != nil || !ok {
		t.Errorf("the concurrent grant did not survive our rollback: %v, %v", ok, err)
	}
}

func TestRestoreGrantsPutsAPriorGrantBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	store := routing.OpenStore(path)
	grant(t, store, "tok-old", "/project/p")

	snap, err := store.SnapshotGrants("/project/p")
	if err != nil {
		t.Fatal(err)
	}
	grant(t, store, "tok-new", "/project/p")
	if err := store.RestoreGrants(snap); err != nil {
		t.Fatal(err)
	}

	g, ok, err := store.Active("/project/p")
	if err != nil || !ok || g.Token != "tok-old" {
		t.Errorf("restored grant = %+v, %v, %v; want tok-old active", g, ok, err)
	}
}

func TestConcurrentEnableRollbacksLoseNoSurvivingGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			suffix := fmt.Sprintf("%02d", i)
			store := routing.OpenStore(path)
			root := "/project/" + suffix
			snap, err := store.SnapshotGrants(root)
			if err != nil {
				errs[i] = err
				return
			}
			if err := store.Grant(routing.Grant{
				Token:         "tok-" + suffix,
				ProjectIDHash: "hash-" + suffix,
				RootPath:      root,
				Upstream:      "https://api.anthropic.com",
				GrantedAt:     "2026-08-01T00:00:00Z",
			}); err != nil {
				errs[i] = err
				return
			}
			// Every odd enable fails and rolls back.
			if i%2 == 1 {
				errs[i] = store.RestoreGrants(snap)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("enable %d: %v", i, err)
		}
	}
	grants, err := routing.OpenStore(path).All()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != n/2 {
		t.Fatalf("%d grants survived, want the %d successful enables", len(grants), n/2)
	}
	for _, g := range grants {
		if (g.Token[len(g.Token)-1]-'0')%2 == 1 {
			t.Errorf("rolled-back grant %s survived", g.Token)
		}
	}
}

func TestPauseByBuildRecordsTheBuildThatPaused(t *testing.T) {
	store, table := openStore(t)
	grant(t, store, "tok-a", "/home/dev/a")
	if err := store.PauseByBuild("redaction_drift", "0.4.0"); err != nil {
		t.Fatal(err)
	}
	if reason, _ := store.PausedReason(); reason != "redaction_drift" {
		t.Errorf("PausedReason = %q, want redaction_drift", reason)
	}
	if _, verdict := table.Lookup("tok-a"); verdict.Decision != routing.ForwardOnlyPaused || verdict.PauseReason != "redaction_drift" {
		t.Errorf("verdict during a pause by build = %+v, want ForwardOnlyPaused with the reason", verdict)
	}
	if resumed, _, err := store.ResumeOtherBuild("redaction_drift", "0.4.0"); err != nil || resumed {
		t.Errorf("the build that paused lifted its own pause: %v, %v", resumed, err)
	}

	if err := store.Resume("signed_out"); err != nil {
		t.Fatal(err)
	}
	if reason, _ := store.PausedReason(); reason != "redaction_drift" {
		t.Errorf("PausedReason after resuming another reason = %q, want the pause kept", reason)
	}
	if err := store.Resume("redaction_drift"); err != nil {
		t.Fatal(err)
	}
	if _, verdict := table.Lookup("tok-a"); !verdict.Records() {
		t.Errorf("verdict after the matching resume = %+v, want Record", verdict)
	}
	if err := store.PauseByBuild("", "0.4.0"); err == nil {
		t.Error("a pause without a reason was accepted")
	}
}
