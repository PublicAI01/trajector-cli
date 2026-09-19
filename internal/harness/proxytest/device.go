package proxytest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
)

// FileTokens keeps this test's device token in the sandbox's own files
// instead of the developer's OS keyring. It is set before the code
// under test opens its token store, which is the only store a test
// never opens for it.
func FileTokens(t *testing.T) {
	t.Helper()
	t.Setenv(tokenstore.BackendEnv, "file")
}

// SetDeviceToken stores a device token, as a completed pairing would.
func (s *Sandbox) SetDeviceToken(token string) {
	s.t.Helper()
	if err := tokenstore.Files(s.layout.SecretsDir()).SetDeviceToken(token); err != nil {
		s.t.Fatal(err)
	}
}

// ClearDeviceToken drops the device token, as signing out would.
func (s *Sandbox) ClearDeviceToken() {
	s.t.Helper()
	if err := tokenstore.Files(s.layout.SecretsDir()).ClearDeviceToken(); err != nil {
		s.t.Fatal(err)
	}
}

// pointAtService writes the user config file that tells a process this
// test spawns which service to upload to.
func (s *Sandbox) pointAtService(url string) {
	s.t.Helper()
	path := s.layout.ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"platform_url":"`+url+`"}`), 0o600); err != nil {
		s.t.Fatal(err)
	}
}
