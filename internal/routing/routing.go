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

// DefaultCacheTTL bounds how stale a cached table may be. Changes on
// disk are picked up within this window plus one request.
const DefaultCacheTTL = time.Second

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
// Exactly two values are legal; each writer resumes only its own, so
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
)

// Explain returns the pause as one user-readable sentence naming the
// command that lifts it. A reason this build does not know (written by
// a newer one) is returned verbatim rather than hidden.
func (r PauseReason) Explain() string {
	switch r {
	case PauseSignedOut:
		return "this device is signed out; run `trajector login` to resume recording"
	case PauseConsentReconfirm:
		return "the data agreement changed; run `trajector enable` to reconfirm it"
	default:
		return string(r)
	}
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
	PausedReason PauseReason              `json:"paused_reason,omitempty"`
	Projects     map[string]projectRecord `json:"projects"`
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
// selects DefaultCacheTTL.
func New(path string, cacheTTL time.Duration) *Table {
	if cacheTTL == 0 {
		cacheTTL = DefaultCacheTTL
	}
	return &Table{path: path, ttl: cacheTTL}
}

// Lookup resolves a token to where its traffic goes and whether this
// exchange may be recorded.
func (t *Table) Lookup(token string) (Route, Verdict) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	r, ok := t.routes[token]
	switch {
	case !ok:
		return Route{}, Verdict{Decision: Unknown}
	case t.revoked[token]:
		return r, Verdict{Decision: ForwardOnlyRevoked}
	case t.pausedReason != "":
		return r, Verdict{Decision: ForwardOnlyPaused, PauseReason: t.pausedReason}
	default:
		return r, Verdict{Decision: Record}
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
