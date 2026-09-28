package lifecycle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// uploadImage stores one call that repeats the image and uploads it
// through the served proxy, and reports how that upload carried the
// image.
func (e *env) uploadImage(t *testing.T, id, x string) string {
	t.Helper()
	e.sandbox.SeedRawcall(id, "hash-images", time.Now().UTC(), proxytest.WithImage(x))
	if err := e.machine().Upload(true, e.io()); err != nil {
		t.Fatalf("upload: %v", err)
	}
	reqs := e.service.Requests()
	return proxytest.BlockForm(t, reqs[len(reqs)-1], x)
}

func TestUploadSendsPlaceholdersWhenTheUserTurnedImageUploadOff(t *testing.T) {
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", fakeplatform.Acknowledges(platform.Handshake{}))
	e.sandbox.WriteUserConfig(`{"upload_images_and_documents":false}`)
	servedProxy(t, e)

	if got := e.uploadImage(t, "req-1", proxytest.ConstructedImage(t)); got != "placeholder" {
		t.Errorf("with image upload turned off the image went up as %s", got)
	}
}

// A command that can leave the service without the images and documents
// this device uploaded must make the device send them in full again:
// a reference to one of them would not resolve. Each test below starts
// from a repeat of an uploaded image going up as a reference, runs the
// command, and checks that the next repeat goes up in full.

// imageUploaded serves the machine's own proxy and makes the service
// take an image and a reference to it.
func (e *env) imageUploaded(t *testing.T, x string) {
	t.Helper()
	e.service.StubFunc("POST", "/v1/batches", fakeplatform.Acknowledges(platform.Handshake{BlockRefs: true}))
	servedProxy(t, e)
	if got := e.uploadImage(t, "req-1", x); got != "original" {
		t.Fatalf("first upload carried the image as %s", got)
	}
	if got := e.uploadImage(t, "req-2", x); got != "reference" {
		t.Fatalf("a repeat of an acknowledged image went up as %s", got)
	}
}

func TestLogoutMakesTheNextUploadSendImagesInFull(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(200, map[string]any{}))
	x := proxytest.ConstructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Logout(e.io()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	// The same token back, so only what logout itself did is tested.
	e.seedDeviceToken()
	if got := e.uploadImage(t, "req-3", x); got != "original" {
		t.Errorf("after logout a repeated image went up as %s", got)
	}
}

func TestPairingMakesTheNextUploadSendImagesInFull(t *testing.T) {
	e := newEnv(t)
	x := proxytest.ConstructedImage(t)
	e.imageUploaded(t, x)

	// A pairing that hands back the very token this device held, so
	// only what pairing itself did is tested.
	e.sandbox.ClearDeviceToken()
	e.pairable()
	if err := e.machine().Login(e.io()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := e.uploadImage(t, "req-3", x); got != "original" {
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
	x := proxytest.ConstructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Login(e.io()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := e.uploadImage(t, "req-3", x); got != "reference" {
		t.Errorf("after a login that changed nothing a repeated image went up as %s", got)
	}
}

func TestPurgeMakesTheNextUploadSendImagesInFull(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/data-deletions", fakeplatform.JSON(202, map[string]any{}))
	x := proxytest.ConstructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Disable(e.project, true, e.io()); err != nil {
		t.Fatalf("disable --purge: %v", err)
	}
	if got := e.uploadImage(t, "req-3", x); got != "original" {
		t.Errorf("after a deletion request a repeated image went up as %s", got)
	}
}

func TestDisableWithoutPurgeKeepsReferringToImages(t *testing.T) {
	e := newEnv(t)
	x := proxytest.ConstructedImage(t)
	e.imageUploaded(t, x)

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := e.uploadImage(t, "req-3", x); got != "reference" {
		t.Errorf("after a disable that deleted nothing uploaded a repeated image went up as %s", got)
	}
}
