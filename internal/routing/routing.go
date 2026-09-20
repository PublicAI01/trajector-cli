// Package routing maps project consent tokens to their capture routes.
// The table is hot-reloaded on modification, so enable and disable take
// effect without restarting the proxy. It answers exactly one question
// per request: where does this traffic forward, and may it be recorded.
package routing

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// defaultCacheTTL bounds how stale a cached table may be. Changes on
// disk are picked up within this window plus one request.
const defaultCacheTTL = time.Second

// Route is what the data path needs to serve one exchange. It carries
// nothing else: the identity of the project and when it consented are
// the CLI's business, not the proxy's.
type Route struct {
	// Upstream keeps receiving this project's traffic even when
	// recording is off, so residual injection cannot break a user's
	// chained third-party setup.
	Upstream      string
	ProjectIDHash string
}

// Decision is what the table concluded about one token.
type Decision string

const (
	// Unknown means the token resolves to nothing: forward at the
	// default upstream and record nothing.
	Unknown Decision = "unknown"
	// Record means forward at the project's upstream and record.
	Record Decision = "record"
	// ForwardOnlyRevoked means the project withdrew consent.
	ForwardOnlyRevoked Decision = "revoked"
	// ForwardOnlyPaused means recording is suspended device-wide while
	// every grant stands.
	ForwardOnlyPaused Decision = "paused"
)

// PauseReason is the device-wide pause written into the routing table.
// Exactly four values are legal; each writer resumes only its own, so
// accepting a new agreement can never silently lift a signed-out pause.
// The machine is the only thing that sets and clears them.
type PauseReason string

const (
	// PauseSignedOut suspends recording while the device holds no
	// pairing token.
	PauseSignedOut PauseReason = "signed_out"
	// PauseConsentReconfirm suspends recording until the changed data
	// agreement is reconfirmed.
	PauseConsentReconfirm PauseReason = "consent_reconfirm"
	// PauseConsentUnreadable suspends recording while the consent
	// record cannot be read or parsed. Whether the agreement in force
	// was accepted is then unknown, and unknown is not yes, so this
	// pause is not the same fact as a changed agreement and must not
	// be reported as one.
	PauseConsentUnreadable PauseReason = "consent_unreadable"
	// PauseRedactionDrift suspends recording when session records took
	// a shape this build's redaction does not cover: what it cannot
	// redact must not leave the device, and a pause is the only way
	// to be sure nothing does.
	PauseRedactionDrift PauseReason = "redaction_drift"
)

// A redaction-drift pause is lifted by two commands in order, and both
// halves of that promise are spelled from these two names: the build is
// replaced first, and only the second command reads the session files
// and decides whether the new build covers them.
const (
	driftFirstStep  = "trajector upgrade"
	driftSecondStep = "trajector doctor"
)

// consentWayOut is the command that writes a new consent record, and
// so the one way out of a record that cannot be read.
const consentWayOut = "trajector enable"

// ExplainUnreadableConsent is the consent_unreadable sentence for a
// surface that read the record itself. The stored reason is one word:
// only the reader of the file can name the file and the failure, so
// the sentence is completed here rather than guessed at by Explain.
func ExplainUnreadableConsent(path string, err error) string {
	return "the consent record at " + path + " could not be read (" + err.Error() + "); run `" + consentWayOut + "` to accept the agreement again"
}

// Explain returns the pause as one user-readable sentence naming the
// command that lifts it. A reason this build does not know (written by
// a newer one) is returned verbatim rather than hidden.
func (r PauseReason) Explain() string {
	switch r {
	case PauseSignedOut:
		return "this device is signed out; run `trajector login` to resume recording"
	case PauseConsentReconfirm:
		return "the data agreement changed; run `trajector enable` to reconfirm it"
	case PauseConsentUnreadable:
		return "the consent record could not be read; run `" + consentWayOut + "` to accept the agreement again"
	case PauseRedactionDrift:
		return "session records changed shape in a way this build's redaction does not cover; run `" + driftFirstStep + "`, then `" + driftSecondStep + "`"
	default:
		return string(r)
	}
}

// ExplainAfterUpgrade returns what this pause still owes the user once
// the build has been replaced, and empty when replacing the build
// advances nothing about it. A newer binary never resumes recording by
// itself, so a command that upgrades one asks here rather than deciding
// for itself which pauses it moved.
func (r PauseReason) ExplainAfterUpgrade() string {
	if r != PauseRedactionDrift {
		return ""
	}
	return "Recording is paused until you run `" + driftSecondStep + "`, which checks that this build can read your session files."
}

// Verdict is the table's answer for one token. Only Record permits
// recording; every other decision still forwards. A pause carries why,
// so surfaces above the proxy can tell the user something they can act
// on instead of "this project will not be recorded".
type Verdict struct {
	Decision Decision
	// PauseReason is set only when Decision is ForwardOnlyPaused.
	PauseReason PauseReason
}

// Records reports whether this exchange may be recorded.
func (v Verdict) Records() bool { return v.Decision == Record }

// Resolves reports whether the token names a project at all.
func (v Verdict) Resolves() bool { return v.Decision != Unknown }

type tableFile struct {
	// PausedReason, when set, suspends recording for every project at
	// once while forwarding continues unchanged. It backs device-wide
	// stops (signed out, consent needs reconfirmation) that must not
	// destroy per-project grants.
	PausedReason PauseReason `json:"paused_reason,omitempty"`
	// PausedByVersion names the build that set PausedReason, when the
	// pause is one that a different build is expected to lift.
	PausedByVersion string                   `json:"paused_by_version,omitempty"`
	Projects        map[string]projectRecord `json:"projects"`
}

type projectRecord struct {
	ProjectIDHash string `json:"project_id_hash"`
	RootPath      string `json:"root_path"`
	Upstream      string `json:"upstream"`
	GrantedAt     string `json:"granted_at"`
	RevokedAt     string `json:"revoked_at,omitempty"`
	// UpstreamMoved records the last unattended upstream change, so a
	// move made where no user was watching stays visible afterwards. A
	// fresh grant clears it: enabling is the user's own baseline.
	UpstreamMoved *upstreamMoveRecord `json:"upstream_moved,omitempty"`
	// NoProxy is how the table stores Shape: it marks a grant whose
	// project injects no base URL, so its traffic never reaches the
	// proxy and only the files its sessions leave are read. The token
	// stays granted so the project is found the same way in either
	// shape.
	NoProxy bool `json:"no_proxy,omitempty"`
}

type upstreamMoveRecord struct {
	From string `json:"from"`
	At   string `json:"at"`
}

// Table is a cached view of the on-disk routing table, safe for
// concurrent lookups.
type Table struct {
	path string
	ttl  time.Duration

	mu           sync.Mutex
	routes       map[string]Route
	revoked      map[string]bool
	pausedReason PauseReason
	checkedAt    time.Time
	mtime        time.Time
	size         int64
	loadErr      error
}

// New returns a table backed by the file at path. A cacheTTL of zero
// selects one second.
func New(path string, cacheTTL time.Duration) *Table {
	if cacheTTL == 0 {
		cacheTTL = defaultCacheTTL
	}
	return &Table{path: path, ttl: cacheTTL}
}

// Lookup resolves a token to where its traffic goes and whether this
// exchange may be recorded.
func (t *Table) Lookup(token string) (Route, Verdict) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	r, known := t.routes[token]
	v := verdictFor(known, t.revoked[token], t.pausedReason)
	if !v.Resolves() {
		return Route{}, v
	}
	return r, v
}

// verdictFor is the one decision of "may this be recorded", from what
// the table holds for a token. Every path that records passes it, so a
// device-wide pause suspends all of them at once and a new reason to
// forward without recording is added in one place.
func verdictFor(known, revoked bool, paused PauseReason) Verdict {
	switch {
	case !known:
		return Verdict{Decision: Unknown}
	case revoked:
		return Verdict{Decision: ForwardOnlyRevoked}
	case paused != "":
		return Verdict{Decision: ForwardOnlyPaused, PauseReason: paused}
	default:
		return Verdict{Decision: Record}
	}
}

// Err reports the most recent load failure, for status and doctor
// surfaces. A missing file is the normal nothing-enabled state, not an
// error.
func (t *Table) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	return t.loadErr
}

func (t *Table) refreshLocked() {
	now := time.Now()
	if !t.checkedAt.IsZero() && now.Sub(t.checkedAt) < t.ttl {
		return
	}
	t.checkedAt = now

	info, err := os.Stat(t.path)
	if err != nil {
		// Consent is unverifiable without a readable table, so no token
		// may resolve; forwarding is unaffected by design.
		t.routes, t.revoked, t.pausedReason = nil, nil, ""
		if os.IsNotExist(err) {
			t.loadErr = nil
		} else {
			t.loadErr = err
		}
		t.mtime, t.size = time.Time{}, 0
		return
	}
	if t.routes != nil && info.ModTime().Equal(t.mtime) && info.Size() == t.size && t.loadErr == nil {
		return
	}

	// Through fsatomic, so the CLI rewriting the table under a live
	// proxy on Windows neither fails its rename against this read nor
	// surfaces here as a spurious load error.
	data, err := fsatomic.ReadFile(t.path)
	if err != nil {
		t.routes, t.revoked, t.loadErr = nil, nil, err
		return
	}
	var f tableFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.routes, t.revoked, t.loadErr = nil, nil, err
		return
	}
	routes := make(map[string]Route, len(f.Projects))
	revoked := make(map[string]bool, len(f.Projects))
	for token, rec := range f.Projects {
		routes[token] = Route{Upstream: rec.Upstream, ProjectIDHash: rec.ProjectIDHash}
		if rec.RevokedAt != "" {
			revoked[token] = true
		}
	}
	t.routes, t.revoked, t.pausedReason, t.loadErr = routes, revoked, f.PausedReason, nil
	t.mtime, t.size = info.ModTime(), info.Size()
}
