// Package routing maps project consent tokens to their capture routes.
// The proxy checks the file on every lookup and reads it again when it
// changed, so enable, disable and a pause take effect on the next
// request without restarting the proxy. It answers exactly one question
// per request: where does this traffic forward, and may it be recorded.
package routing

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

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
	// ForwardOnlyUnreadable means the table cannot be read now, and the
	// last table this process read names the token: forward at the
	// upstream that table recorded and record nothing. Which grants
	// stand now is unknown, and unknown is not consent.
	ForwardOnlyUnreadable Decision = "unreadable"
	// NeverRead means the table cannot be read and this process has
	// never read it, so where the token's traffic goes is not known at
	// all. Nothing may be forwarded for it: the default upstream is a
	// guess, and for a project chained to a third-party relay the guess
	// sends the relay's credential headers to the official endpoint.
	NeverRead Decision = "never_read"
)

// PauseReason is the device-wide pause written into the routing table.
// The legal values are the ones AllPauseReasons returns; each writer
// resumes only its own, so accepting a new agreement can never silently
// lift a signed-out pause. The machine is the only thing that sets and
// clears them.
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
	// PauseRedactionDrift suspends recording when a read of a session
	// file contradicted itself: it handed over a line cut short of its
	// newline, which a reader never does. Nothing that read produced
	// can be trusted to be masked, and a pause is the only way to be
	// sure none of it leaves the device. A line of a shape this build
	// does not know sets no pause: only its own segment is held on
	// this machine. The stored value keeps its first name, because a
	// table written by an earlier build carries it.
	PauseRedactionDrift PauseReason = "redaction_drift"
)

// DoctorCommand is the command that looks into this device and repairs
// what it can. Every surface that sends a user to it reads this one
// spelling, and it is spelled here because the pause this table stores
// is lifted by it.
const DoctorCommand = "trajector doctor"

// The commands that lift a pause are named once here. A redaction-drift
// pause is lifted by two of them in order, and both halves of that
// promise are spelled from these names: the build is replaced first,
// and only the second command reads the session files again and
// decides whether the build now returns every line whole. consentWayOut
// writes a new consent record, and so is the one way out of a record
// that cannot be read.
const (
	signedOutWayOut = "trajector login"
	consentWayOut   = "trajector enable"
	driftFirstStep  = "trajector upgrade"
	driftSecondStep = DoctorCommand
)

// ReEnableCaveat is the one spelling of the clause a sentence carries
// when it sends an already enabled project back to `trajector enable`.
// A bare run declares the shape with a proxy and collects the earlier
// session files, so following such a sentence converts a project in the
// shape without a proxy, and collects the earlier session files of a
// project enabled with them left alone, unless the sentence tells the
// user to name both flags again.
const ReEnableCaveat = "(with --no-proxy and --no-earlier if you use them)"

// CollectEarlierCaveat is the clause a sentence carries in place of
// ReEnableCaveat when the enable it names is there to collect a
// project's earlier session files. That run must omit --no-earlier, so
// the clause names only the shape flag.
const CollectEarlierCaveat = "(with --no-proxy if you use it)"

// AllPauseReasons returns every pause value this build knows, in the
// order a surface that answers for all of them should read. It is what
// makes "the legal values" countable: a new reason is one constant, one
// entry here, and every surface that enumerates picks it up.
//
//exportsurface:keep test support: tests check every surface against every pause reason this build knows
func AllPauseReasons() []PauseReason {
	return []PauseReason{
		PauseSignedOut, PauseConsentReconfirm, PauseConsentUnreadable, PauseRedactionDrift,
	}
}

// Why states what stopped recording and nothing about how to end it: a
// surface with room for three lines prints this one and the commands
// separately. A reason this build does not know (written by a newer
// one) is returned verbatim rather than hidden.
func (r PauseReason) Why() string {
	switch r {
	case PauseSignedOut:
		return "this device is signed out"
	case PauseConsentReconfirm:
		return "the data agreement changed and has not been reconfirmed"
	case PauseConsentUnreadable:
		return "the consent record could not be read"
	case PauseRedactionDrift:
		return "a session file line was read without the newline that ends it, so what was read cannot be trusted"
	default:
		return string(r)
	}
}

// WhyAt is Why with what only the surface that read the consent record
// can add: the file and the failure. The stored reason is one word, so
// the sentence is completed by its reader rather than guessed at here.
// Every other reason needs nothing added and answers as Why does.
func (r PauseReason) WhyAt(path string, err error) string {
	if r != PauseConsentUnreadable || err == nil {
		return r.Why()
	}
	return "the consent record at " + path + " could not be read (" + err.Error() + ")"
}

// Fix returns the commands that lift the pause, in the order they must
// be run. Each element is a whole command line the user can copy: a
// surface with room for one command prints the first and states the
// rest as detail. An unknown reason names no command.
func (r PauseReason) Fix() []string {
	switch r {
	case PauseSignedOut:
		return []string{signedOutWayOut}
	case PauseConsentReconfirm, PauseConsentUnreadable:
		return []string{consentWayOut}
	case PauseRedactionDrift:
		return []string{driftFirstStep, driftSecondStep}
	default:
		return nil
	}
}

// FixCaveat returns what the user must know about the commands Fix
// names that a command line they copy whole cannot carry, and empty
// when there is nothing. Every surface that names those commands
// states it next to them.
func (r PauseReason) FixCaveat() string {
	if slices.Contains(r.Fix(), consentWayOut) {
		return ReEnableCaveat
	}
	return ""
}

// Explain returns the pause as one user-readable sentence naming the
// commands that lift it: what a surface with room for a single line
// prints. It is Why, Fix and FixCaveat joined, so the one-line form and
// the three-line form cannot come to say different things.
func (r PauseReason) Explain() string { return explain(r.Why(), r.Fix(), r.FixCaveat()) }

// ExplainAt is Explain for a surface that read the consent record
// itself, in the sentence Explain uses.
func (r PauseReason) ExplainAt(path string, err error) string {
	return explain(r.WhyAt(path, err), r.Fix(), r.FixCaveat())
}

func explain(why string, fix []string, caveat string) string {
	if len(fix) == 0 {
		return why
	}
	sentence := why + "; run `" + strings.Join(fix, "`, then `") + "`"
	if caveat != "" {
		sentence += " " + caveat
	}
	return sentence
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

// Resolves reports whether the token names a project: whether the
// Route returned beside the verdict says where to forward.
func (v Verdict) Resolves() bool { return v.Decision != Unknown && v.Decision != NeverRead }

// Forwards reports whether this exchange may be forwarded at all. It is
// false only for NeverRead.
func (v Verdict) Forwards() bool { return v.Decision != NeverRead }

type tableFile struct {
	// PausedReason, when set, suspends recording for every project at
	// once while forwarding continues unchanged. It backs device-wide
	// stops that must not destroy per-project grants.
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
	// NoEarlier marks a grant made with the project's earlier session
	// files left alone: they were never registered, so nothing reads
	// them. Like every other field here it is additive — a table
	// written by a build that does not know it reads as a grant that
	// collected them, which is what such a build did.
	NoEarlier bool `json:"no_earlier,omitempty"`
}

type upstreamMoveRecord struct {
	From string `json:"from"`
	At   string `json:"at"`
}

// Table is a cached view of the on-disk routing table, safe for
// concurrent lookups. Every lookup checks the file first: a request that
// starts after a write to the table is decided by that write, because a
// `disable` deletes the project's unuploaded records right after it
// revokes the token, and a record decided from the table before the
// revoke would land after the deletion and be uploaded. Only a file that
// changed is read again; see refreshLocked for what counts as a change.
//
// While the file cannot be read, the table keeps
// what it last read: every token that table named keeps forwarding
// where it was granted to, and none is recorded.
//
// A missing file is a file that cannot be read. After this process read
// the table, a missing file is most likely one moved aside by hand, while
// the injections it routed still stand; reading it as an empty table
// would send every one of those tokens to the default upstream. Before
// this process read it, a missing file places no token: a request that
// carries one is refused, and a request that carries none goes to the
// default upstream as always.
type Table struct {
	path string

	mu sync.Mutex
	// routes, revoked and pausedReason are the last table this process
	// read. read reports that such a read happened at all.
	routes       map[string]Route
	revoked      map[string]bool
	pausedReason PauseReason
	read         bool
	file         os.FileInfo
	loadErr      error
}

// Lookup resolves a token to where its traffic goes and whether this
// exchange may be recorded.
func (t *Table) Lookup(token string) (Route, Verdict) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	r, known := t.routes[token]
	var v Verdict
	switch {
	case t.loadErr == nil:
		v = verdictFor(known, t.revoked[token], t.pausedReason)
	case known:
		v = Verdict{Decision: ForwardOnlyUnreadable}
	case !t.read:
		v = Verdict{Decision: NeverRead}
	default:
		// The last table read did not name the token either, so the
		// token is unknown as it was then: the default upstream is
		// where such traffic went while the table could be read.
		v = Verdict{Decision: Unknown}
	}
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

// refreshLocked reads the file again when it is not the file this table
// last read. The file changed when its modification time or its size
// differs, or when the path names another file: a Store write replaces
// the table by renaming a new file over it, and two such writes within
// one tick of the file system's clock can leave the same time and size
// behind. An edit made in place, by hand, that keeps both the size and
// the time within one tick is the one change this does not see. On
// Windows the identity check opens the file on each call that finds it
// unchanged; see sameFile.
func (t *Table) refreshLocked() {
	info, err := os.Stat(t.path)
	if err != nil {
		// Consent is unverifiable without a readable table, so nothing
		// records; the last table read still says where traffic goes.
		t.loadErr = &UnreadableError{Path: t.path, Err: err}
		t.file = nil
		return
	}
	if t.read && t.loadErr == nil && sameFile(t.file, info) {
		return
	}
	pinIdentity(info)

	// Through fsatomic, so the CLI rewriting the table under a live
	// proxy on Windows neither fails its rename against this read nor
	// surfaces here as a spurious load error.
	data, err := fsatomic.ReadFile(t.path)
	if err != nil {
		t.loadErr = &UnreadableError{Path: t.path, Err: err}
		return
	}
	var f tableFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.loadErr = &UnreadableError{Path: t.path, Err: err}
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
	t.routes, t.revoked, t.pausedReason, t.read, t.loadErr = routes, revoked, f.PausedReason, true, nil
	t.file = info
}

// sameFile reports that now describes the file last describes, unchanged.
// On Windows, os.SameFile reads the identity of now by opening the file,
// so every lookup that finds the file unchanged opens one more handle;
// that cost is accepted, since the identity is what tells a renamed-over
// table from the one last read.
func sameFile(last, now os.FileInfo) bool {
	return last != nil && now.ModTime().Equal(last.ModTime()) && now.Size() == last.Size() && os.SameFile(last, now)
}

// pinIdentity makes info hold the identity of the file its path names
// now. On Windows, os.Stat leaves the identity unread and os.SameFile
// reads it from the path at the first comparison; by then a rename may
// have put another file there, and the stale description would compare
// equal to the new file.
func pinIdentity(info os.FileInfo) {
	_ = os.SameFile(info, info)
}
