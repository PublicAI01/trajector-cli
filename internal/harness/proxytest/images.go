package proxytest

import (
	"bytes"
	"io"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// UploadImage stores one recorded call whose request carries payload as
// an image, and uploads what the spool holds to service, with the
// device token this sandbox stores, the way a proxy started now would:
// an uploader of its own over the sandbox's files. It reports how that
// upload carried the image: "original", "reference", or "placeholder".
// The calls of every UploadImage are of one session and one project.
func (s *Sandbox) UploadImage(service *fakeplatform.Server, id, payload string) string {
	s.t.Helper()
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}`
	s.SeedRawcall(id, "hash-images", time.Now(), func(o *Observation) {
		o.Request = []byte(`{"model":"claude-fable-5","metadata":{"user_id":"sess-images"},"messages":[{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_01","content":[` + image + `]}]}]}`)
	})
	sp, err := spool.Open(s.layout.SpoolDir(), 0)
	if err != nil {
		s.t.Fatal(err)
	}
	u, err := upload.New(upload.Deps{
		Spool:   sp,
		Service: platform.New(service.URL(), "test"),
		DeviceToken: func() (string, error) {
			token, _, err := tokenstore.Files(s.layout.SecretsDir()).DeviceToken()
			return token, err
		},
		Version:     "test",
		Dir:         s.layout.UploadDir(),
		RejectedDir: s.layout.RejectedDir(),
	})
	if err != nil {
		s.t.Fatal(err)
	}
	if res, err := u.Flush(true); err != nil || res.Outcome != upload.Uploaded {
		s.t.Fatalf("uploading the image: %+v, %v", res, err)
	}
	reqs := service.Requests()
	parts, err := fakeplatform.Parts(reqs[len(reqs)-1])
	if err != nil {
		s.t.Fatal(err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(parts["records"]))
	if err != nil {
		s.t.Fatal(err)
	}
	defer zr.Close()
	stream, err := io.ReadAll(zr)
	if err != nil {
		s.t.Fatal(err)
	}
	switch text := string(stream); {
	case strings.Contains(text, payload):
		return "original"
	case strings.Contains(text, `"type":"sha256_ref"`):
		return "reference"
	case strings.Contains(text, `"type":"omitted"`):
		return "placeholder"
	}
	s.t.Fatal("the upload carries no copy of the image")
	return ""
}
