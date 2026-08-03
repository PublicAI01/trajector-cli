package tokenstore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
)

func TestDeviceTokenLifecycle(t *testing.T) {
	s := tokenstore.Files(filepath.Join(t.TempDir(), "secrets"))

	if _, ok, err := s.DeviceToken(); ok || err != nil {
		t.Fatalf("DeviceToken before login = %v, %v, want signed out", ok, err)
	}
	if err := s.SetDeviceToken("sk-test-fake"); err != nil {
		t.Fatal(err)
	}
	token, ok, err := s.DeviceToken()
	if err != nil || !ok || token != "sk-test-fake" {
		t.Fatalf("DeviceToken = %q, %v, %v", token, ok, err)
	}
	if err := s.ClearDeviceToken(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.DeviceToken(); ok || err != nil {
		t.Errorf("DeviceToken after clear = %v, %v, want signed out", ok, err)
	}
	if err := s.ClearDeviceToken(); err != nil {
		t.Errorf("clearing an already-cleared token = %v, want nil", err)
	}
}

func TestOpenHonorsTheFileBackendEscapeHatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	t.Setenv(tokenstore.BackendEnv, "file")

	s := tokenstore.Open(dir)
	if err := s.SetDeviceToken("sk-test-fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "device.secret")); err != nil {
		t.Errorf("secret not stored under the file backend: %v", err)
	}
	token, ok, err := s.DeviceToken()
	if err != nil || !ok || token != "sk-test-fake" {
		t.Errorf("DeviceToken = %q, %v, %v", token, ok, err)
	}
}
