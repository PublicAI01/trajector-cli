package upload

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/PublicAI01/trajector-cli/internal/mediablock"
)

// sentBlocksName is the file that remembers which image and document
// payloads the service already holds. It is not among BookkeepingFiles:
// it names digests of what the user's records held, and a diagnostic
// archive has no use for them.
const sentBlocksName = "sent-blocks.json"

// The file is bounded. Forgetting a payload costs only its next copy
// going up in full again; remembering one the service does not hold
// would lose it. So the bound evicts, and nothing else is ever done to
// make room.
const (
	// maxSentSessions is how many sessions are remembered, the ones
	// most recently acknowledged.
	maxSentSessions = 32
	// maxSentPerSession is how many payloads one session remembers,
	// the most recently acknowledged.
	maxSentPerSession = 1024
)

// sentBlocks is what the file holds: per session, the digests of the
// payloads an acknowledged batch carried in full, least recent first.
// A session is named by a digest of its scope, not by the scope, so the
// file holds no session identity of its own.
//
// Only an acknowledgement adds to it, and only one that said the service
// puts references back. That is the whole of what makes a reference
// safe: every digest here was taken by a service that can resolve it.
// A file that is lost, cut, or unreadable reads as empty, and the next
// copy of each payload goes up in full.
//
// What the file holds is true of one place only: the service this
// device uploads to, under the account its device token belongs to, as
// that account's data stood when the payloads were acknowledged. Holder
// names that place (see holderOf), and the file is only read in the
// place it names. Anything that can change the place, or take away
// data the service held there, invalidates the file:
//
//   - another device token: pairing again, or signing out and in, can
//     put the device under another account;
//   - another service: the service address this device uploads to;
//   - a deletion this device asks for: the service deletes the data the
//     payloads were in. ForgetSentBlocks records such an event;
//   - a deletion this device cannot see, made on the service's side:
//     the service says so by a new epoch in its acknowledgements.
//
// Each of them changes the holder, so the file stops being read at
// once, whichever process caused the change.
type sentBlocks struct {
	Holder   string        `json:"holder"`
	Sessions []sentSession `json:"sessions"`
}

type sentSession struct {
	Scope   string   `json:"scope"`
	Digests []string `json:"digests"`
}

func loadSentBlocks(dir string) *sentBlocks {
	var s sentBlocks
	readJSON(filepath.Join(dir, sentBlocksName), &s)
	return &s
}

func (s *sentBlocks) save(dir string) error {
	return writeJSON(filepath.Join(dir, sentBlocksName), s)
}

// sentBlocksEpochName is the file that names the last event on this
// device that took away data the service held: see ForgetSentBlocks.
// Like sent-blocks.json, it is not among BookkeepingFiles: it is a
// random value and tells a diagnosis nothing.
const sentBlocksEpochName = "sent-blocks.epoch"

type sentBlocksEpoch struct {
	Epoch string `json:"epoch"`
}

// ForgetSentBlocks makes the record of which payloads the service
// holds useless from now on, in this process and in every other. Call
// it for every event on this device after which the service may no
// longer hold what it acknowledged: signing out, pairing, and a request
// to delete uploaded data. The next copy of each payload then goes up
// in full. Call it before the event, where it can go first: a failure
// here must not leave the event done and the record still read.
func ForgetSentBlocks(dir string) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, sentBlocksEpochName), sentBlocksEpoch{Epoch: hex.EncodeToString(nonce[:])}); err != nil {
		return err
	}
	// The new epoch alone makes the old record unread, so emptying it
	// is only tidying, and a failure to empty it is no failure of this
	// call. It is replaced, not removed: on Windows a file that another
	// process holds open cannot be removed.
	_ = (&sentBlocks{}).save(dir)
	return nil
}

// holderOf names the place that holds what the file records: the
// service address, the device token, this device's epoch, and the
// epoch the service stated. It is a digest, so the file never holds
// the token. Each part is quoted, so no two lists of parts give one
// name.
func holderOf(dir, service, token, serviceEpoch string) string {
	var epoch sentBlocksEpoch
	readJSON(filepath.Join(dir, sentBlocksEpochName), &epoch)
	parts := []string{service, token, epoch.Epoch, serviceEpoch}
	var name []byte
	for _, part := range parts {
		name = strconv.AppendQuote(name, part)
	}
	sum := sha256.Sum256(name)
	return hex.EncodeToString(sum[:])
}

// sessionName is how the file names a scope.
func sessionName(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:16])
}

// has reports whether an acknowledged batch carried this payload in
// full in this scope.
func (s *sentBlocks) has(scope, digest string) bool {
	name := sessionName(scope)
	for _, session := range s.Sessions {
		if session.Scope == name {
			return slices.Contains(session.Digests, digest)
		}
	}
	return false
}

// add remembers the payloads one acknowledged batch carried in full.
// The sessions it touches become the most recent, and whatever falls
// past either bound is forgotten.
func (s *sentBlocks) add(originals []mediablock.Original) {
	for _, o := range originals {
		name := sessionName(o.Scope)
		i := slices.IndexFunc(s.Sessions, func(session sentSession) bool { return session.Scope == name })
		session := sentSession{Scope: name}
		if i >= 0 {
			session = s.Sessions[i]
			s.Sessions = slices.Delete(s.Sessions, i, i+1)
		}
		if j := slices.Index(session.Digests, o.Digest); j >= 0 {
			session.Digests = slices.Delete(session.Digests, j, j+1)
		}
		session.Digests = append(session.Digests, o.Digest)
		if over := len(session.Digests) - maxSentPerSession; over > 0 {
			session.Digests = slices.Delete(session.Digests, 0, over)
		}
		s.Sessions = append(s.Sessions, session)
	}
	if over := len(s.Sessions) - maxSentSessions; over > 0 {
		s.Sessions = slices.Delete(s.Sessions, 0, over)
	}
}
