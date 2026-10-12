package selfupdate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// writeState puts content where in's state file lives.
func writeState(t *testing.T, in selfupdate.Installation, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(in.State), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.State, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// stateFields is the state file as JSON fields, whatever build wrote
// them.
func stateFields(t *testing.T, in selfupdate.Installation) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(in.State)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("the state file is not JSON: %v\n%s", err, data)
	}
	return fields
}

func loadState(t *testing.T, in selfupdate.Installation) selfupdate.State {
	t.Helper()
	s, err := selfupdate.LoadState(in.State)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAMachineThatHasNotUpgradedHasNoState(t *testing.T) {
	s, err := selfupdate.LoadState(filepath.Join(t.TempDir(), "upgrade.json"))
	if err != nil || !reflect.DeepEqual(s, selfupdate.State{}) {
		t.Errorf("LoadState = %+v, %v; want the zero state", s, err)
	}
}

func TestAStateFileThatIsNotJSONCannotBeLoaded(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	writeState(t, m.at("0.1.0"), "{not json")
	if _, err := selfupdate.LoadState(m.at("0.1.0").State); err == nil {
		t.Error("LoadState read a file that is not JSON")
	}
}

func TestAFailedUpgradeRecordsItsErrorWithoutTheURLQuery(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	// A download URL can carry a signature in its query: a credential
	// for that download that has no place in a file status prints.
	index := releases.IndexURL() + "-elsewhere?X-Amz-Signature=deadbeef&token=secret#part"

	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), index, 0); err == nil {
		t.Fatal("Upgrade succeeded against a source with no index")
	}
	got := loadState(t, m.at("0.1.0")).Error
	if !strings.Contains(got, releases.IndexURL()+"-elsewhere") || !strings.Contains(got, "not published") {
		t.Errorf("recorded error is %q, want the failure and where it happened", got)
	}
	if strings.ContainsAny(got, "?#\n") || strings.Contains(got, "secret") {
		t.Errorf("recorded error is %q, want it on one line without the query", got)
	}

	// A run that ends without an error says so by clearing it.
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if got := loadState(t, m.at("0.1.0")).Error; got != "" {
		t.Errorf("recorded error is %q after a run that did not fail", got)
	}
}

func TestRecordingKeepsFieldsANewerBuildWroteAndDropsPassedVersions(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	writeState(t, m.at("0.1.0"), `{
		"checked_at": "2026-10-12T08:00:00Z",
		"finish_pending": "0.1.0",
		"installed": "0.1.0",
		"unusable": {"0.0.5": "it cannot be run: exec format error", "0.1.0": "x", "0.4.0": "it exited with exit status 1"},
		"error": "an earlier failure"
	}`)
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))

	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	fields := stateFields(t, m.at("0.1.0"))
	// A build that runs after a newer one has used the file — the one
	// still running while it installs its successor — must not erase
	// what it does not know about.
	if got := string(fields["checked_at"]); got != `"2026-10-12T08:00:00Z"` {
		t.Errorf("checked_at is %s, want the newer build's field carried over", got)
	}
	if _, ok := fields["error"]; ok {
		t.Errorf("error is %s after a run that did not fail", fields["error"])
	}
	s := loadState(t, m.at("0.1.0"))
	want := map[string]string{"0.4.0": "it exited with exit status 1"}
	if !reflect.DeepEqual(s.Unusable, want) || s.Installed != "0.1.0" {
		t.Errorf("state is %+v, want only the version ahead of 0.1.0 left unusable", s)
	}
	// A run that commits nothing leaves an unfinished install named for
	// the build that has to finish it.
	if s.FinishPending != "0.1.0" {
		t.Errorf("finish_pending is %q after a run that installed nothing, want 0.1.0 kept", s.FinishPending)
	}
}

func TestRecordingReplacesAStateFileThatIsNotJSON(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	writeState(t, m.at("0.1.0"), "{not json")
	releases := fakereleases.New(t)

	// A note nobody can read must not stop the bookkeeping for good.
	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0); err == nil {
		t.Fatal("Upgrade succeeded against a source that has published nothing")
	}
	if got := loadState(t, m.at("0.1.0")).Error; !strings.Contains(got, "no usable version") {
		t.Errorf("recorded error is %q, want this run's failure", got)
	}
}

func TestTheStateFileIsPrivateToTheUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no permission bits to check")
	}
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	info, err := os.Stat(m.at("0.1.0").State)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file mode is %v, want 0600", perm)
	}
}
