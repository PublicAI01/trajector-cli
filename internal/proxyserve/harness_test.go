package proxyserve_test

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
)

// env is one isolated device: temp directories standing in for the
// user's machine, a fake service, and the assembly a serving process
// would be built from on it.
type env struct {
	t        *testing.T
	assembly proxyserve.Assembly
	service  *fakeplatform.Server
	sandbox  *proxytest.Sandbox
	client   *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	layout := proxytest.SandboxLayout(t, t.TempDir())
	service := fakeplatform.New(t)
	tokens := tokenstore.Files(layout.SecretsDir())
	if err := tokens.SetDeviceToken("dev-tok-fake"); err != nil {
		t.Fatal(err)
	}
	return &env{
		t:       t,
		service: service,
		sandbox: proxytest.Open(t, layout),
		client:  proxytest.Client(t),
		assembly: proxyserve.Assembly{
			Layout:   layout,
			Tokens:   tokens,
			Service:  platform.New(service.URL(), "testv"),
			Consent:  consent.Open(layout.ConsentFile()),
			Version:  "testv",
			ExecPath: t.TempDir() + "/trajector",
			Addr:     freeAddr(t),
		},
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// occupyPort binds the proxy address with a server that is not a
// trajector proxy.
func (e *env) occupyPort() {
	e.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { l.Close() })
	go http.Serve(l, http.NotFoundHandler())
	e.assembly.Addr = l.Addr().String()
}

// occupyPortStillPublishing binds the proxy address with a holder that
// leaves the first challenge unproven and proves itself from the next
// one on, as a sibling between winning its bind and publishing its
// admin token would.
func (e *env) occupyPortStillPublishing() {
	e.t.Helper()
	im := proxytest.StartImposter(e.t, proxytest.Health{Service: apiproxy.ServiceName, Version: e.assembly.Version})
	const token = "feedfacefeedfacefeedfacefeedface"
	proxytest.PublishAdminToken(e.t, e.assembly.Layout, im.Addr(), token)
	im.ProveAfter(1, token)
	e.assembly.Addr = im.Addr()
}

func (e *env) waitHealthy() {
	e.t.Helper()
	proxytest.WaitServing(e.t, e.client, e.assembly.Addr, e.assembly.Layout)
}

// adminPost posts to the served proxy's reserved endpoint with the
// admin token it published.
func (e *env) adminPost(path string) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+e.assembly.Addr+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	proxytest.Authorize(req, e.assembly.Layout)
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
}

func (e *env) flush() proxytest.FlushReply {
	e.t.Helper()
	return proxytest.Flush(e.t, e.client, e.assembly.Addr, e.assembly.Layout)
}

// uploadedBatch is one recorded upload: which batch id carried which
// spool records.
type uploadedBatch struct {
	BatchID    string
	RequestIDs []string
}

// parseBatch reads the batch id and request ids one upload carried.
func parseBatch(r fakeplatform.Request) (uploadedBatch, error) {
	parts, err := fakeplatform.Parts(r)
	if err != nil {
		return uploadedBatch{}, err
	}
	var env struct {
		BatchID string `json:"batch_id"`
		Records []struct {
			RequestID string `json:"request_id"`
		} `json:"records"`
	}
	if err := json.Unmarshal(parts["batch"], &env); err != nil {
		return uploadedBatch{}, err
	}
	if env.BatchID == "" {
		return uploadedBatch{}, errors.New("no batch id in envelope")
	}
	b := uploadedBatch{BatchID: env.BatchID}
	for _, rec := range env.Records {
		b.RequestIDs = append(b.RequestIDs, rec.RequestID)
	}
	return b, nil
}

// ackBatch acknowledges an upload under the batch id it carried, the
// way the live service answers a well-formed batch.
//
// TODO: converge the sibling ack builders (echoAck in upload,
// stubEchoAck in cli, ackBatch in lifecycle and here) into fakeplatform
// the next time ack semantics change.
func ackBatch(r fakeplatform.Request) fakeplatform.Response {
	b, err := parseBatch(r)
	if err != nil {
		return fakeplatform.JSON(590, map[string]any{"error": err.Error()})
	}
	return fakeplatform.JSON(200, map[string]any{"batch_id": b.BatchID})
}

// aged is a capture old enough that the upload thresholds no longer
// hold it back.
func aged() time.Time { return time.Now().UTC().Add(-25 * time.Hour) }
