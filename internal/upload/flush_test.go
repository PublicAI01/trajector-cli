package upload_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func TestFlushHandlerServesTheWireContract(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.uploader.Handler("test-proxy"))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+upload.FlushPath+"?force=1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply upload.FlushReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Service != "test-proxy" || reply.Outcome != upload.Empty || reply.Error != "" {
		t.Errorf("reply = %+v, want the empty-spool outcome from test-proxy", reply)
	}

	get, err := srv.Client().Get(srv.URL + upload.FlushPath)
	if err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Errorf("GET flush = %d, want 404", get.StatusCode)
	}
	other, err := srv.Client().Post(srv.URL+"/trajector/other", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("unknown mounted path = %d, want 404", other.StatusCode)
	}
}

func TestFlushHandlerCarriesTheOutcomeWithTheError(t *testing.T) {
	f := newFixture(t)
	f.storeRawcall(t, "req-1", f.now)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}))
	srv := httptest.NewServer(f.uploader.Handler("test-proxy"))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+upload.FlushPath+"?force=1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply upload.FlushReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Outcome != upload.UpgradeRequired || reply.MinClientVersion != "9.9.9" {
		t.Errorf("reply = %+v, want the classified outcome with the required version", reply)
	}
	if reply.Error == "" {
		t.Errorf("reply = %+v, want the error kept as detail", reply)
	}
}

func TestFlushHandlerReportsAFailedFlush(t *testing.T) {
	f := newFixture(t)
	f.storeRawcall(t, "req-1", f.now)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(500, map[string]any{"error": "down"}))
	srv := httptest.NewServer(f.uploader.Handler("test-proxy"))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+upload.FlushPath+"?force=1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply upload.FlushReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error == "" {
		t.Errorf("reply = %+v, want the flush failure carried in the reply", reply)
	}
}
