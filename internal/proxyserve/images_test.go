package proxyserve_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// lastUploadForm reports how the latest upload carried payload.
func (e *env) lastUploadForm(payload string) string {
	e.t.Helper()
	reqs := e.service.Requests()
	return proxytest.BlockForm(e.t, reqs[len(reqs)-1], payload)
}

func TestServeReadsTheImageSwitchForEveryBatch(t *testing.T) {
	payload := proxytest.ConstructedImage(t)

	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", fakeplatform.Acknowledges(platform.Handshake{}))
	var logs bytes.Buffer
	served := e.serve(io.Discard, &logs)
	e.waitHealthy()

	steps := []struct {
		config string
		want   string
	}{
		{"", "original"},
		{`{"upload_images_and_documents":false}`, "placeholder"},
		{`{"upload_images_and_documents":false,`, "placeholder"},
		{`{"upload_images_and_documents":true}`, "original"},
	}
	for i, step := range steps {
		if step.config != "" {
			e.sandbox.WriteUserConfig(step.config)
		}
		e.sandbox.SeedRawcall(fmt.Sprintf("req-%d", i), "hash-p1", time.Now().UTC(), proxytest.WithImage(payload))
		if reply := e.flush(); reply.Outcome != proxytest.Uploaded {
			t.Fatalf("step %d: flush = %+v", i, reply)
		}
		if got := e.lastUploadForm(payload); got != step.want {
			t.Fatalf("step %d, config %q: the upload carried the image as %s, want %s", i, step.config, got, step.want)
		}
	}
	e.adminPost(apiproxy.DrainPath)
	e.waitExit(served, 10*time.Second)
	if !strings.Contains(logs.String(), "images and documents go up as placeholders until it reads") {
		t.Errorf("an unreadable user config file was not reported:\n%s", logs.String())
	}
}

func TestServeSendsPlaceholdersWhileTheUserConfigFileCannotBeRead(t *testing.T) {
	payload := proxytest.ConstructedImage(t)

	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", fakeplatform.Acknowledges(platform.Handshake{}))
	e.sandbox.WriteUserConfig(`{"upload_images_and_documents":true}`)
	var logs bytes.Buffer
	served := e.serve(io.Discard, &logs)
	e.waitHealthy()

	path := e.assembly.Layout.ConfigFile()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this user can read a file with no permissions")
	}

	e.sandbox.SeedRawcall("req-unreadable", "hash-p1", time.Now().UTC(), proxytest.WithImage(payload))
	if reply := e.flush(); reply.Outcome != proxytest.Uploaded {
		t.Fatalf("flush = %+v", reply)
	}
	if got := e.lastUploadForm(payload); got != "placeholder" {
		t.Fatalf("the image went up as %s while the file that turns its upload on or off could not be read", got)
	}
	e.adminPost(apiproxy.DrainPath)
	e.waitExit(served, 10*time.Second)
	if !strings.Contains(logs.String(), "images and documents go up as placeholders until it reads") {
		t.Errorf("an unreadable user config file was not reported with what it does:\n%s", logs.String())
	}
}
