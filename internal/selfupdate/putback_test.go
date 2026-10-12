package selfupdate_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// releaseIndex is a release index that publishes nothing and counts
// how many times it was asked.
func releaseIndex(t *testing.T) (url string, asked *atomic.Int32) {
	t.Helper()
	asked = new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/releases", asked
}

func TestUpgrade_AfterAnInterruptedUpgradePutsTheKeptBinaryBackAndDoesNothingElse(t *testing.T) {
	// The 0.2.0 build was put in place and never accepted its install;
	// the user runs upgrade from it.
	m := install(t, "the 0.2.0 build, never checked")
	plant(t, m.kept("0.1.0"), "the 0.1.0 binary", time.Now())
	writeState(t, m.at("0.1.0"), `{"installed": "0.1.0"}`)
	index, asked := releaseIndex(t)

	_, err := selfupdate.Upgrade(t.Context(), m.at("0.2.0"), index, 0)
	putBack, ok := errors.AsType[*selfupdate.PutBackError](err)
	if !ok || putBack.Version != "0.1.0" {
		t.Fatalf("Upgrade = %v, want 0.1.0 put back", err)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("the binary is %q, want the kept 0.1.0 binary put back", got)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the release index was asked %d times by a build no longer installed", n)
	}
	if s := loadState(t, m.at("0.1.0")); s.Installed != "0.1.0" {
		t.Errorf("state is %+v, want 0.1.0 still installed", s)
	}
}
