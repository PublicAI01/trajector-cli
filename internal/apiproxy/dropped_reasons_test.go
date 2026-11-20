package apiproxy_test

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestRawcallRefusedByAnUnwritableSpoolDirectoryIsCountedAsPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory modes do not refuse this user")
	}
	e := proxytest.New(t)
	e.WriteTable(activeTable(e.Upstream.URL()))
	spoolDir := e.Layout().SpoolDir()
	if err := os.Chmod(spoolDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(spoolDir, 0o700) })

	e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_refused"}`)})
	if resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil); resp.StatusCode != 200 {
		t.Fatalf("forwarding broken by the spool failure: %d", resp.StatusCode)
	}

	h := e.WaitHealthz(func(h proxytest.Health) bool { return len(h.RecentRecordingErrors) > 0 })
	if !strings.HasSuffix(h.RecentRecordingErrors[0], " spool permission denied") {
		t.Errorf("recorded error = %q, want the category alone", h.RecentRecordingErrors[0])
	}
	reasons := e.Dropped().Snapshot().Reasons
	if len(reasons) != 1 || reasons["spool permission denied"].Count != 1 {
		t.Errorf("dropped captures = %+v, want one under spool permission denied", reasons)
	}
}
