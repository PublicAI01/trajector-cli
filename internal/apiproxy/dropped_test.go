package apiproxy_test

import (
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestTwoProxiesOfOneDeviceKeepEachOthersCountOfRefusedRawcalls(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	older := proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithFullSpool())
	newer := proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithFullSpool())
	older.WriteTable(activeTable(older.Upstream.URL()))

	for _, e := range []*proxytest.Env{older, newer} {
		older.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_1"}`)})
		if reply := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil); reply.StatusCode != 200 {
			t.Fatalf("exchange through a proxy with a full spool = %d, want 200", reply.StatusCode)
		}
		e.WaitHealthz(func(h proxytest.Health) bool { return h.CapturesDropped == 1 })
	}
	for _, e := range []*proxytest.Env{older, newer} {
		e.PostAdmin(apiproxy.DrainPath)
		if err := e.WaitStopped(10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}

	rec, err := apiproxy.ReadDropped(layout.DroppedCaptures())
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.SpoolFull().Count; got != 2 {
		t.Errorf("rawcalls refused by the full spool on disk = %d, want 2: one from each proxy", got)
	}
}
