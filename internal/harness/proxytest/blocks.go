package proxytest

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/mediablock"
)

// ConstructedImage is a few dozen bytes of valid PNG, base64-encoded,
// never a real screenshot.
func ConstructedImage(t testing.TB) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// WithImage makes a seeded rawcall's request repeat payload as a PNG
// image in a tool result, the way every call after a Read of it does.
// Every call it seeds is of one session, so the calls of one project
// and one UTC date share their images' scope.
func WithImage(payload string) RawcallOption {
	return func(obs *Observation) {
		obs.Request = []byte(`{"model":"claude-fable-5","metadata":{"user_id":"sess-images"},"messages":[{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_01","content":[` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}]}]}]}`)
	}
}

// BlockForm reports how one recorded upload carried payload:
// "original", "reference", or "placeholder". An upload that carries no
// copy of it fails the test.
func BlockForm(t testing.TB, r fakeplatform.Request, payload string) string {
	t.Helper()
	stream, err := fakeplatform.UploadedRecords(r)
	if err != nil {
		t.Fatal(err)
	}
	switch text := string(stream); {
	case strings.Contains(text, payload):
		return "original"
	case strings.Contains(text, `"type":"`+mediablock.SourceReference+`"`):
		return "reference"
	case strings.Contains(text, `"type":"`+mediablock.SourceOmitted+`"`):
		return "placeholder"
	}
	t.Fatal("the upload carries no copy of the image")
	return ""
}
