package proxytest

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// The uploader's own files, seeded here so a test can start from a
// device that has already talked to the service. upload exports every
// reader of these files but no writer, so the layout is spelled once —
// here — and every seeder reads its work back through upload's readers:
// a spelling that drifts from the uploader's fails at the seed, not in
// whatever the test went on to assert.
const handshakeFile = "handshake.json"

// Handshake is what the service tunes on this client alongside an
// acknowledged upload, in the client's own type.
type Handshake = platform.Handshake

// SeedHandshake persists what the service last said about its settings,
// as an acknowledged upload would have left it. A setting left zero
// keeps what the file already recorded, matching the merge the uploader
// applies to a handshake that speaks about one setting only.
func (s *Sandbox) SeedHandshake(h Handshake) {
	s.t.Helper()
	s.overlayHandshake(h)
	if got := upload.LoadHandshake(s.layout.UploadDir()); got == (Handshake{}) && h != (Handshake{}) {
		s.t.Fatalf("the uploader reads back no handshake after seeding %+v", h)
	}
}

// upgradeRefusal is what the file records about a version refusal: the
// minimum in the handshake's own field, the wording beside it.
type upgradeRefusal struct {
	Handshake
	UpgradeMessage string `json:"upgrade_message,omitempty"`
}

// SeedUpgradeRefusal persists the service refusing this client's
// version, as an upload that met a 426 would have left it. It does not
// disturb an authorization refusal already recorded: both refusals can
// stand at the same time.
func (s *Sandbox) SeedUpgradeRefusal(minClientVersion, message string) {
	s.t.Helper()
	s.overlayHandshake(upgradeRefusal{
		Handshake:      Handshake{MinClientVersion: minClientVersion},
		UpgradeMessage: message,
	})
	dir := s.layout.UploadDir()
	if got, want := upload.LoadHandshake(dir).MinClientVersion, platform.SafeServiceText(minClientVersion); got != want {
		s.t.Fatalf("the uploader reads the seeded minimum version back as %q, want %q", got, want)
	}
	if got, want := upload.LoadUpgradeMessage(dir), platform.SafeServiceText(message); got != want {
		s.t.Fatalf("the uploader reads the seeded refusal wording back as %q, want %q", got, want)
	}
}

// authorizationRefusal is what the file records about an authorization
// refusal. The fact is stored on its own because the service may supply
// neither detail, and a refusal that said nothing is still a refusal.
type authorizationRefusal struct {
	AuthorizationRequired bool   `json:"authorization_required"`
	AuthorizeURL          string `json:"authorize_url,omitempty"`
	AuthorizationMessage  string `json:"authorization_message,omitempty"`
}

// SeedAuthorizationRefusal persists the service refusing this account's
// uploads until its data authorization is complete, as an upload that
// met a 451 would have left it. Like SeedUpgradeRefusal it leaves the
// other refusal alone.
func (s *Sandbox) SeedAuthorizationRefusal(authorizeURL, message string) {
	s.t.Helper()
	s.overlayHandshake(authorizationRefusal{
		AuthorizationRequired: true,
		AuthorizeURL:          authorizeURL,
		AuthorizationMessage:  message,
	})
	got := upload.LoadAuthorizationNotice(s.layout.UploadDir())
	want := upload.AuthorizationNotice{
		Required: true,
		URL:      platform.SafeServiceURL(authorizeURL),
		Message:  platform.SafeServiceText(message),
	}
	if got != want {
		s.t.Fatalf("the uploader reads the seeded refusal back as %+v, want %+v", got, want)
	}
}

// ForgetHandshake removes everything the service has said to this
// device, leaving a machine that has never had an answer from it.
func (s *Sandbox) ForgetHandshake() {
	s.t.Helper()
	err := os.Remove(filepath.Join(s.layout.UploadDir(), handshakeFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.t.Fatal(err)
	}
}

// overlayHandshake merges update into the stored handshake the way the
// uploader merges what the service says onto what it already holds: a
// field the caller left zero is absent from the encoded update, so the
// stored value survives it. Merging through the encoded form rather
// than field by field keeps the rule stated once, in the tags of the
// types above.
func (s *Sandbox) overlayHandshake(update any) {
	s.t.Helper()
	path := filepath.Join(s.layout.UploadDir(), handshakeFile)
	stored := map[string]json.RawMessage{}
	if data, err := fsatomic.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &stored); err != nil {
			s.t.Fatal(err)
		}
	}
	for name, value := range s.statedFields(update) {
		stored[name] = value
	}
	data, err := json.Marshal(stored)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.t.Fatal(err)
	}
	if err := fsatomic.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// statedFields is v as the fields it actually states.
func (s *Sandbox) statedFields(v any) map[string]json.RawMessage {
	s.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		s.t.Fatal(err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		s.t.Fatal(err)
	}
	return fields
}

// Rejection is why a batch was set aside, in the uploader's own type.
type Rejection = upload.Rejection

// Cause is which side set a batch aside, with both values re-exported
// so CLI-layer tests name a scenario without importing the domain
// package.
type Cause = upload.Cause

const (
	CauseRefused    = upload.CauseRefused
	CauseUnreadable = upload.CauseUnreadable
)

// RejectedBatch is one quarantined batch as the rejected store holds it.
type RejectedBatch = upload.RejectedBatch

// reasonFile names the recorded reason inside a quarantined batch.
const reasonFile = "reason.json"

// QuarantineBatch sets records aside under one batch id, the way a
// service rejection would have left them. The records are the bytes as
// they were spooled, keyed by request id — a test that wants one to be
// unreadable passes bytes that are not a rawcall. An empty At gets a
// fixed timestamp and an unstated record count is the number of records
// passed: most tests do not care about either.
func (s *Sandbox) QuarantineBatch(rej Rejection, records map[string][]byte) {
	s.t.Helper()
	if rej.At.IsZero() {
		rej.At = seedTime
	}
	if rej.Records == 0 {
		rej.Records = len(records)
	}
	dir := filepath.Join(s.layout.RejectedDir(), rej.BatchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.t.Fatal(err)
	}
	for requestID, data := range records {
		if err := fsatomic.WriteFile(filepath.Join(dir, requestID+".json"), data, 0o600); err != nil {
			s.t.Fatal(err)
		}
	}
	reason, err := json.Marshal(rej)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := fsatomic.WriteFile(filepath.Join(dir, reasonFile), append(reason, '\n'), 0o600); err != nil {
		s.t.Fatal(err)
	}
	for _, b := range s.QuarantinedBatches() {
		if b.BatchID == rej.BatchID && b.Records == len(records) {
			return
		}
	}
	s.t.Fatalf("the uploader does not read batch %s back with %d record(s)", rej.BatchID, len(records))
}

// QuarantinedBatches reports every batch waiting in the rejected store,
// read the way the surfaces that warn about them read it.
func (s *Sandbox) QuarantinedBatches() []RejectedBatch {
	s.t.Helper()
	batches, err := upload.ListRejected(s.layout.RejectedDir())
	if err != nil {
		s.t.Fatal(err)
	}
	return batches
}

// QuarantinedRecords counts the rawcalls waiting across every
// quarantined batch.
func (s *Sandbox) QuarantinedRecords() int {
	s.t.Helper()
	records := 0
	for _, b := range s.QuarantinedBatches() {
		records += b.Records
	}
	return records
}
