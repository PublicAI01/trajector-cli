package routing_test

import (
	"os"
	"path/filepath"
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

func TestMissingFileMeansNoRoutesAndNoError(t *testing.T) {
	table := routing.New(filepath.Join(t.TempDir(), "routes-under-test.json"), 0)
	if _, verdict := table.Lookup("tok"); verdict.Resolves() {
		t.Error("token resolved without a table file")
	}
	if err := table.Err(); err != nil {
		t.Errorf("Err = %v, want nil for the nothing-enabled state", err)
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
