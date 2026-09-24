package routing_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func writeTable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lookupOnceChanged looks token up until the verdict satisfies want, and
// returns the last answer after a second either way. A table keeps a
// read for its cache TTL, and on a platform whose clock ticks coarsely
// two lookups made one after the other can fall inside one tick, so the
// first lookup after the file changes may still answer from the read
// before it.
func lookupOnceChanged(table *routing.Table, token string, want func(routing.Verdict) bool) (routing.Route, routing.Verdict) {
	deadline := time.Now().Add(time.Second)
	for {
		route, verdict := table.Lookup(token)
		if want(verdict) || time.Now().After(deadline) {
			return route, verdict
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLookupResolvesActiveAndRevokedRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{
		"projects": {
			"tok-active": {
				"project_id_hash": "hash-a",
				"root_path": "/home/dev/project-a",
				"upstream": "https://api.anthropic.com",
				"granted_at": "2026-08-01T00:00:00Z"
			},
			"tok-revoked": {
				"project_id_hash": "hash-b",
				"root_path": "/home/dev/project-b",
				"upstream": "https://relay.example.com",
				"granted_at": "2026-07-01T00:00:00Z",
				"revoked_at": "2026-07-15T00:00:00Z"
			}
		}
	}`)
	table := routing.New(path, 0)

	active, verdict := table.Lookup("tok-active")
	if verdict.Decision != routing.Record {
		t.Fatalf("active token verdict = %+v, want Record", verdict)
	}
	want := routing.Route{ProjectIDHash: "hash-a", Upstream: "https://api.anthropic.com"}
	if active != want {
		t.Errorf("active route = %+v, want %+v", active, want)
	}

	revoked, verdict := table.Lookup("tok-revoked")
	if verdict.Decision != routing.ForwardOnlyRevoked {
		t.Fatalf("revoked token verdict = %+v, want ForwardOnlyRevoked", verdict)
	}
	if !verdict.Resolves() || verdict.Records() {
		t.Error("a revoked token must still resolve for forwarding, with recording off")
	}
	if revoked.Upstream != "https://relay.example.com" {
		t.Errorf("revoked route upstream = %q, want the recorded upstream", revoked.Upstream)
	}

	if _, verdict := table.Lookup("tok-unknown"); verdict.Decision != routing.Unknown {
		t.Errorf("unknown token verdict = %+v", verdict)
	}
	if err := table.Err(); err != nil {
		t.Errorf("Err = %v, want nil", err)
	}
}

func TestLookupServesCachedTableWithinTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{"projects":{"tok":{"project_id_hash":"h1","upstream":"https://api.anthropic.com"}}}`)
	table := routing.New(path, time.Hour)

	if _, verdict := table.Lookup("tok"); !verdict.Resolves() {
		t.Fatal("token not found before rewrite")
	}
	writeTable(t, path, `{"projects":{}}`)
	if _, verdict := table.Lookup("tok"); !verdict.Resolves() {
		t.Error("cached table discarded before TTL elapsed")
	}
}

func TestLookupPicksUpChangesAfterTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{"projects":{"tok-old":{"project_id_hash":"h1","upstream":"https://api.anthropic.com"}}}`)
	table := routing.New(path, time.Millisecond)

	if _, verdict := table.Lookup("tok-old"); !verdict.Resolves() {
		t.Fatal("initial token not found")
	}
	writeTable(t, path, `{"projects":{"tok-new":{"project_id_hash":"h2","upstream":"https://api.anthropic.com","granted_at":"2026-08-01T00:00:00Z"}}}`)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, verdict := table.Lookup("tok-new"); verdict.Resolves() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rewritten table not picked up after TTL")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, verdict := table.Lookup("tok-old"); verdict.Resolves() {
		t.Error("removed token still resolves after reload")
	}
}

func TestMalformedTableResolvesNothingAndReportsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{"projects": {`)
	table := routing.New(path, time.Millisecond)

	if _, verdict := table.Lookup("tok"); verdict.Resolves() {
		t.Error("token resolved from a malformed table")
	}
	if err := table.Err(); err == nil {
		t.Error("Err = nil, want the parse failure")
	}

	writeTable(t, path, `{"projects":{"tok":{"project_id_hash":"h","upstream":"https://api.anthropic.com"}}}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, verdict := table.Lookup("tok"); verdict.Resolves() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("table did not recover after the file was fixed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := table.Err(); err != nil {
		t.Errorf("Err = %v after recovery, want nil", err)
	}
}

func TestATableThatBreaksKeepsTheRoutesItLastReadAndRecordsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{"projects":{"tok":{"project_id_hash":"h","upstream":"https://relay.example.com"}}}`)
	table := routing.New(path, time.Nanosecond)
	if _, verdict := table.Lookup("tok"); !verdict.Records() {
		t.Fatalf("verdict = %+v, want the readable grant recorded", verdict)
	}

	writeTable(t, path, `{"projects": {`)
	route, verdict := lookupOnceChanged(table, "tok", func(v routing.Verdict) bool { return !v.Records() })
	if verdict.Decision != routing.ForwardOnlyUnreadable || verdict.Records() || !verdict.Forwards() {
		t.Errorf("verdict = %+v, want forwarding without recording", verdict)
	}
	if route.Upstream != "https://relay.example.com" {
		t.Errorf("route = %+v, want the upstream the last table read recorded", route)
	}
	if _, verdict := table.Lookup("other"); verdict.Decision != routing.Unknown {
		t.Errorf("verdict for a token the last table did not name = %+v, want unknown", verdict)
	}
}

func TestATableUnreadableSinceItWasOpenedPlacesNoToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{"projects": {`)
	table := routing.New(path, time.Nanosecond)

	route, verdict := table.Lookup("tok")
	if verdict.Decision != routing.NeverRead || verdict.Forwards() || verdict.Resolves() || verdict.Records() {
		t.Errorf("verdict = %+v, want nowhere to forward", verdict)
	}
	if route != (routing.Route{}) {
		t.Errorf("route = %+v, want none", route)
	}
}

func TestATableMissingSinceItWasOpenedPlacesNoToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	table := routing.New(path, time.Nanosecond)

	route, verdict := table.Lookup("tok")
	if verdict.Decision != routing.NeverRead || verdict.Forwards() || verdict.Resolves() || verdict.Records() {
		t.Errorf("verdict = %+v, want nowhere to forward", verdict)
	}
	if route != (routing.Route{}) {
		t.Errorf("route = %+v, want none", route)
	}
	if err := table.Err(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Err = %v, want the missing file", err)
	}

	writeTable(t, path, `{"projects": {`)
	if _, verdict := table.Lookup("tok"); verdict.Decision != routing.NeverRead {
		t.Errorf("verdict = %+v, want nowhere to forward while no table was ever read", verdict)
	}

	writeTable(t, path, `{"projects":{"tok":{"project_id_hash":"h","upstream":"https://relay.example.com"}}}`)
	if _, verdict := lookupOnceChanged(table, "tok", routing.Verdict.Records); !verdict.Records() {
		t.Errorf("verdict = %+v, want the grant recorded once the table appears", verdict)
	}
}

func TestATableMovedAsideKeepsTheRoutesItLastReadAndRecordsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{"projects":{"tok":{"project_id_hash":"h","upstream":"https://relay.example.com"}}}`)
	table := routing.New(path, time.Nanosecond)
	if _, verdict := table.Lookup("tok"); !verdict.Records() {
		t.Fatalf("verdict = %+v, want the readable grant recorded", verdict)
	}

	aside := path + ".aside"
	if err := os.Rename(path, aside); err != nil {
		t.Fatal(err)
	}
	route, verdict := lookupOnceChanged(table, "tok", func(v routing.Verdict) bool { return !v.Records() })
	if verdict.Decision != routing.ForwardOnlyUnreadable || verdict.Records() || !verdict.Forwards() {
		t.Errorf("verdict = %+v, want forwarding without recording", verdict)
	}
	if route.Upstream != "https://relay.example.com" {
		t.Errorf("route = %+v, want the upstream the last table read recorded", route)
	}
	if _, verdict := table.Lookup("other"); verdict.Decision != routing.Unknown {
		t.Errorf("verdict for a token the last table did not name = %+v, want unknown", verdict)
	}
	if unreadable, ok := errors.AsType[*routing.UnreadableError](table.Err()); !ok || unreadable.Path != path {
		t.Errorf("Err = %v, want the table at %s unreadable", table.Err(), path)
	}

	if err := os.Rename(aside, path); err != nil {
		t.Fatal(err)
	}
	if _, verdict := lookupOnceChanged(table, "tok", routing.Verdict.Records); !verdict.Records() {
		t.Errorf("verdict = %+v, want recording back once the table is back", verdict)
	}
}

func TestDeviceWidePauseKeepsGrantsButStopsRecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes-under-test.json")
	writeTable(t, path, `{
		"paused_reason": "signed_out",
		"projects": {
			"tok": {
				"project_id_hash": "h1",
				"root_path": "/home/dev/project",
				"upstream": "https://relay.example.com",
				"granted_at": "2026-08-01T00:00:00Z"
			}
		}
	}`)

	route, verdict := routing.New(path, 0).Lookup("tok")
	if verdict.Decision != routing.ForwardOnlyPaused {
		t.Fatalf("verdict = %+v, want ForwardOnlyPaused", verdict)
	}
	if verdict.PauseReason != "signed_out" {
		t.Errorf("PauseReason = %q, want the reason to survive the lookup", verdict.PauseReason)
	}
	if verdict.Records() {
		t.Error("a paused device recorded")
	}
	if !verdict.Resolves() || route.Upstream != "https://relay.example.com" {
		t.Errorf("route = %+v, want the grant intact so forwarding is unchanged", route)
	}
}

func TestEveryPauseNamesAStopAndTheCommandsThatEndIt(t *testing.T) {
	seen := map[routing.PauseReason]bool{}
	for _, reason := range routing.AllPauseReasons() {
		if seen[reason] {
			t.Fatalf("%s listed twice", reason)
		}
		seen[reason] = true
		explained := reason.Explain()
		if why := reason.Why(); why == "" || !strings.HasPrefix(explained, why) {
			t.Errorf("%s: why = %q, explained as %q", reason, why, explained)
		}
		fix := reason.Fix()
		if len(fix) == 0 {
			t.Fatalf("%s names no command", reason)
		}
		for _, command := range fix {
			if !strings.HasPrefix(command, "trajector ") {
				t.Errorf("%s: fix %q, want a command a user can run", reason, command)
			}
			if !strings.Contains(explained, command) {
				t.Errorf("%s: explained as %q, want it to name %q", reason, explained, command)
			}
		}
	}
}

func TestAPauseFromANewerBuildIsPassedThroughUntouched(t *testing.T) {
	future := routing.PauseReason("something_this_build_never_heard_of")
	if got := future.Why(); got != string(future) {
		t.Errorf("why = %q, want the stored value", got)
	}
	if got := future.Fix(); got != nil {
		t.Errorf("fix = %q, want no command guessed at", got)
	}
	if got := future.Explain(); got != string(future) {
		t.Errorf("explained as %q, want the stored value", got)
	}
	if got := future.ExplainAfterUpgrade(); got != "" {
		t.Errorf("after an upgrade it says %q, want nothing claimed for a reason this build cannot read", got)
	}
}

func TestOnlyTheReaderOfTheConsentRecordNamesTheFileAndTheFailure(t *testing.T) {
	const path = "/home/dev/.trajector/consent.json"
	failure := errors.New("permission denied")

	reason := routing.PauseConsentUnreadable
	why := reason.WhyAt(path, failure)
	if !strings.Contains(why, path) || !strings.Contains(why, failure.Error()) {
		t.Errorf("why = %q, want the file and the failure in it", why)
	}
	if explained := reason.ExplainAt(path, failure); !strings.HasPrefix(explained, why) || !strings.Contains(explained, reason.Fix()[0]) {
		t.Errorf("explained as %q, want %q and the command that ends it", explained, why)
	}
	if got := reason.WhyAt("", nil); got != reason.Why() {
		t.Errorf("with nothing read, why = %q, want %q", got, reason.Why())
	}
	if got := routing.PauseSignedOut.WhyAt(path, failure); got != routing.PauseSignedOut.Why() {
		t.Errorf("a signed-out pause says %q, want %q", got, routing.PauseSignedOut.Why())
	}
}
