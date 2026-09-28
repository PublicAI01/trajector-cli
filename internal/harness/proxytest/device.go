package proxytest

import (
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
