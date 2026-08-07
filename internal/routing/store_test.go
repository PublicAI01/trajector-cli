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

func TestGrantReplacesPreviousEntryForSameRoot(t *testing.T) {
	store, table := openStore(t)
	grant(t, store, "tok-old", "/home/dev/project")
	if err := store.Revoke("/home/dev/project", "2026-08-01T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	grant(t, store, "tok-new", "/home/dev/project")

	if _, verdict := table.Lookup("tok-old"); verdict.Resolves() {
		t.Error("re-enable kept the old token resolvable")
	}
	if _, verdict := table.Lookup("tok-new"); !verdict.Records() {
		t.Errorf("new token verdict = %+v, want Record", verdict)
	}
	all, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("entries after re-enable = %d, want 1", len(all))
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
