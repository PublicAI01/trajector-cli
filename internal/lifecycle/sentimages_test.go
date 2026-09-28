package lifecycle_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// A command that can leave the service without the images and documents
// this device uploaded must make the device send them in full again:
// a reference to one of them would not resolve. Each test starts from a
// repeat of an uploaded image going up as a reference, runs the
// command, and checks that the next repeat goes up in full.

func constructedImage(t *testing.T) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// imageUploaded makes the service take an image and a reference to it.
func (e *env) imageUploaded(t *testing.T, x string) {
	t.Helper()
	e.service.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	if got := e.sandbox.UploadImage(e.service, "req-1", x); got != "original" {
		t.Fatalf("first upload carried the image as %s", got)
	}
	if got := e.sandbox.UploadImage(e.service, "req-2", x); got != "reference" {
		t.Fatalf("a repeat of an acknowledged image went up as %s", got)
	}
}

func TestLogoutMakesTheNextUploadSendImagesInFull(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(200, map[string]any{}))
	x := constructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Logout(e.io()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	// The same token back, so only what logout itself did is tested.
	e.seedDeviceToken()
	if got := e.sandbox.UploadImage(e.service, "req-3", x); got != "original" {
		t.Errorf("after logout a repeated image went up as %s", got)
	}
}

func TestPairingMakesTheNextUploadSendImagesInFull(t *testing.T) {
	e := newEnv(t)
	x := constructedImage(t)
	e.imageUploaded(t, x)

	// A pairing that hands back the very token this device held, so
	// only what pairing itself did is tested.
	e.sandbox.ClearDeviceToken()
	e.pairable()
	if err := e.machine().Login(e.io()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := e.sandbox.UploadImage(e.service, "req-3", x); got != "original" {
		t.Errorf("after pairing again a repeated image went up as %s", got)
	}
}

func TestPairingDoesNotStartWhenTheRecordOfSentImagesCannotBeForgotten(t *testing.T) {
	e := newUnpairedEnv(t)
	e.pairable()
	uploadDir := e.layout().UploadDir()
	if err := os.MkdirAll(filepath.Dir(uploadDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uploadDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().Login(e.io()); err == nil {
		t.Fatal("login succeeded although the record of sent images could not be forgotten")
	}
	for _, r := range e.service.Requests() {
		if strings.HasPrefix(r.URL, "/v1/pairings") {
			t.Errorf("the service was asked %s %s before the record of sent images was forgotten", r.Method, r.URL)
		}
	}
}

func TestLoginOnAPairedDeviceKeepsReferringToImages(t *testing.T) {
	e := newEnv(t)
	x := constructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Login(e.io()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := e.sandbox.UploadImage(e.service, "req-3", x); got != "reference" {
		t.Errorf("after a login that changed nothing a repeated image went up as %s", got)
	}
}

func TestPurgeMakesTheNextUploadSendImagesInFull(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/data-deletions", fakeplatform.JSON(202, map[string]any{}))
	x := constructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Disable(e.project, true, e.io()); err != nil {
		t.Fatalf("disable --purge: %v", err)
	}
	if got := e.sandbox.UploadImage(e.service, "req-3", x); got != "original" {
		t.Errorf("after a deletion request a repeated image went up as %s", got)
	}
}

func TestDisableWithoutPurgeKeepsReferringToImages(t *testing.T) {
	e := newEnv(t)
	x := constructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := e.sandbox.UploadImage(e.service, "req-3", x); got != "reference" {
		t.Errorf("after a disable that deleted nothing uploaded a repeated image went up as %s", got)
	}
}
