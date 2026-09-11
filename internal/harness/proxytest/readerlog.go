package proxytest

import (
	"os"

	"github.com/PublicAI01/trajector-cli/internal/drift"
)

// ReaderLogEntry is one entry of what reading this device's session
// files noticed, in the type of the module that owns the log.
type ReaderLogEntry = drift.LogEntry

// ReaderLog reports every entry the readers of this device logged,
// oldest first. A device whose readers found nothing has none.
func (s *Sandbox) ReaderLog() []ReaderLogEntry {
	s.t.Helper()
	entries, err := drift.ReadLog(s.layout.ReaderLog())
	if err != nil {
		s.t.Fatal(err)
	}
	return entries
}

// ReaderLogText is the log as it lies on disk, for a test that states
// what the file must never hold. A log nothing has written yet is
// empty.
func (s *Sandbox) ReaderLogText() string {
	s.t.Helper()
	data, err := os.ReadFile(s.layout.ReaderLog())
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return string(data)
}
