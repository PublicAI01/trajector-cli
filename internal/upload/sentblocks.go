package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"

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
type sentBlocks struct {
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
