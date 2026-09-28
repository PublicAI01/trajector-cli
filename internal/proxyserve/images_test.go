package proxyserve_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// withImage makes a seeded rawcall's request carry the payload in an
// image block.
func withImage(payload string) proxytest.RawcallOption {
	return func(obs *proxytest.Observation) {
		obs.Request = []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":[` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}]}]}`)
	}
}

func (e *env) writeUserConfig(content string) {
	e.t.Helper()
	path := e.assembly.Layout.ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// lastUploadedStream decompresses the records of the latest upload.
func (e *env) lastUploadedStream() string {
	e.t.Helper()
	reqs := e.service.Requests()
	parts, err := fakeplatform.Parts(reqs[len(reqs)-1])
	if err != nil {
		e.t.Fatal(err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(parts["records"]))
	if err != nil {
		e.t.Fatal(err)
	}
	defer zr.Close()
	stream, err := io.ReadAll(zr)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(stream)
}

func TestServeReadsTheImageSwitchForEveryBatch(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(buf.Bytes())

	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch)
	var logs bytes.Buffer
	served := e.serve(io.Discard, &logs)
	e.waitHealthy()

	steps := []struct {
		config string
		want   string
	}{
		{"", `"type":"base64"`},
		{`{"upload_images_and_documents":false}`, `"type":"omitted"`},
		{`{"upload_images_and_documents":false,`, `"type":"omitted"`},
		{`{"upload_images_and_documents":true}`, `"type":"base64"`},
	}
	for i, step := range steps {
		if step.config != "" {
			e.writeUserConfig(step.config)
		}
		e.sandbox.SeedRawcall(fmt.Sprintf("req-%d", i), "hash-p1", time.Now().UTC(), withImage(payload))
		if reply := e.flush(); reply.Outcome != proxytest.Uploaded {
			t.Fatalf("step %d: flush = %+v", i, reply)
		}
		stream := e.lastUploadedStream()
		if !strings.Contains(stream, step.want) {
			t.Fatalf("step %d, config %q: the upload does not carry %s", i, step.config, step.want)
		}
		if step.want == `"type":"omitted"` && strings.Contains(stream, payload) {
			t.Fatalf("step %d: the image left the machine with upload turned off", i)
		}
	}
	e.adminPost(apiproxy.DrainPath)
	e.waitExit(served, 10*time.Second)
	if !strings.Contains(logs.String(), "images and documents go up as placeholders until it reads") {
		t.Errorf("an unreadable user config file was not reported:\n%s", logs.String())
	}
}

func TestServeSendsPlaceholdersWhileTheUserConfigFileCannotBeRead(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(buf.Bytes())

	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{}))
	e.writeUserConfig(`{"upload_images_and_documents":true}`)
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

	e.sandbox.SeedRawcall("req-unreadable", "hash-p1", time.Now().UTC(), withImage(payload))
	if reply := e.flush(); reply.Outcome != proxytest.Uploaded {
		t.Fatalf("flush = %+v", reply)
	}
	stream := e.lastUploadedStream()
	if strings.Contains(stream, payload) || !strings.Contains(stream, `"type":"omitted"`) {
		t.Fatalf("the image left the machine while the file that turns its upload on or off could not be read:\n%s", stream)
	}
	e.adminPost(apiproxy.DrainPath)
	e.waitExit(served, 10*time.Second)
	if !strings.Contains(logs.String(), "images and documents go up as placeholders until it reads") {
		t.Errorf("an unreadable user config file was not reported with what it does:\n%s", logs.String())
	}
}
