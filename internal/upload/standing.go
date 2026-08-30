package upload

import (
	"fmt"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/semver"
)

// Reason is why this client is or is not offering uploads right now.
// The set is closed: every answer to "why is nothing uploading" is one
// of these values, and a condition that maps to none of them is a
// condition this client cannot yet explain rather than one it may
// describe in a renderer's own words.
//
// It is not the same idea as routing's device-wide recording pause, and
// the two are worded apart on purpose: a recording pause stops rawcalls
// from being written at all, while a standing only stops what is
// already spooled from being offered.
type Reason string

const (
	// Flowing is the zero value: nothing is holding uploads back.
	Flowing Reason = ""
	// SignedOut: the device holds no pairing token, so there is nothing
	// to upload with. Captured data is kept.
	SignedOut Reason = "signed_out"
	// VersionGate: the minimum client version the service last stated is
	// one this build does not demonstrably meet, or the service refused
	// this build outright.
	VersionGate Reason = "version_gate"
	// AuthorizationGate: the service will not take this account's uploads
	// until its data authorization is complete. The user completes it off
	// this machine; nothing local has to change for uploads to resume.
	AuthorizationGate Reason = "authorization_gate"
	// RateLimited: the service asked this client to slow down and named
	// when it may try again.
	RateLimited Reason = "rate_limited"
	// TimedOut: the last attempt ran out of its time budget, so the next
	// one waits and is allowed more time. It is a reason of its own
	// because the pause is this client's own decision, not the service's,
	// and a user reading it is looking at their link rather than at the
	// service.
	TimedOut Reason = "timed_out"
	// QuarantineOnly: the spool holds nothing to send and every rawcall
	// left on this machine is waiting in quarantine.
	QuarantineOnly Reason = "quarantine_only"
)

// Standing is one reason uploads are held back, with everything the
// reason needs to explain itself. It is the single source of both
// sentences a surface prints about that reason — what is true and what
// ends it — so status, doctor, and the bundle cannot word one condition
// three ways.
//
// A flush carries the one standing that stopped it; a diagnosis carries
// every standing currently held, because the two refusal gates can
// stand at the same time and a user told about only one of them would
// go and fix half of what is wrong.
type Standing struct {
	Reason Reason `json:"reason"`
	// MinClientVersion is the stated minimum this build does not
	// demonstrably meet, empty when the service stated none or when this
	// build meets it. Emptied at the one place the comparison is made, so
	// a renderer states the requirement exactly when there is one to
	// state without comparing versions itself.
	MinClientVersion string `json:"min_client_version,omitempty"`
	// Version is this build, so the sentence can name both sides of the
	// requirement.
	Version string `json:"version,omitempty"`
	// Message is what the service said about the refusal, in its own
	// words, empty when it said nothing. Relayed, never parsed.
	Message string `json:"message,omitempty"`
	// AuthorizeURL is where the user completes the data authorization,
	// empty when the service named no usable address.
	AuthorizeURL string `json:"authorize_url,omitempty"`
	// NotBefore is when automatic uploads may attempt again. Set on the
	// two pauses that expire; zero on the gates, which end when the
	// condition behind them does.
	NotBefore time.Time `json:"not_before,omitzero"`
	// Upgradable reports that installing the newest release is known to
	// answer this version gate. It is decided once, where the standing is
	// built, so no renderer repeats the comparison.
	Upgradable bool `json:"upgradable,omitempty"`
	// Refused reports that the service answered a refusal to the attempt
	// this standing came out of, rather than the standing being read back
	// off disk. Uploads really are stopped in the first case; in the
	// second the client only knows a requirement was stated, which it may
	// well already meet by the time anyone reads it.
	Refused bool `json:"refused,omitempty"`
}

// Held reports a standing that is holding uploads back.
func (s Standing) Held() bool { return s.Reason != Flowing }

// Explain states what is true, in one sentence. A reason this build
// does not know — written by a newer one into a file this one reads —
// is returned verbatim rather than hidden.
func (s Standing) Explain() string {
	switch s.Reason {
	case SignedOut:
		return "Uploads are paused: this device is signed out."
	case VersionGate:
		// One clause, framed two ways: a refusal in hand has stopped
		// uploads, while a minimum read off disk has only been stated —
		// saying uploads were paused there would claim more than is known.
		clause := "the service refuses this build's version"
		if s.MinClientVersion != "" {
			clause = fmt.Sprintf("the service requires client version %s or newer; this build is %s", s.MinClientVersion, s.Version)
		}
		if s.Refused {
			return "Uploads are paused: " + clause + "."
		}
		return strings.ToUpper(clause[:1]) + clause[1:] + "."
	case AuthorizationGate:
		return "Uploads are paused: this account's data authorization is not complete."
	case RateLimited:
		return fmt.Sprintf("Uploads are paused until %s: the service asked to slow down.", s.pauseUntil())
	case TimedOut:
		return fmt.Sprintf("Uploads are paused until %s: the last attempt ran out of time.", s.pauseUntil())
	case QuarantineOnly:
		return "Uploads have nothing to send: every rawcall left on this machine is quarantined."
	default:
		return string(s.Reason)
	}
}

// Remedy names what ends this standing, empty when nothing the user can
// run would move it.
//
// Two reasons deliberately carry none. An unorderable version pair — a
// development build, or a stated minimum that is not a semantic version
// — states its requirement without a remedy, because `trajector
// upgrade` has nothing to install for such a build and would answer the
// user with that. QuarantineOnly carries none because the quarantine
// finding printed beside it already names the commands that end the
// wait, and a second spelling of them would be one too many.
func (s Standing) Remedy() string {
	switch s.Reason {
	case SignedOut:
		return "Run `trajector login` to pair this device; uploads resume then."
	case VersionGate:
		if !s.Upgradable {
			return ""
		}
		return "Run `trajector upgrade` to install the newest release."
	case AuthorizationGate:
		// The address is named only when the service supplied one. The
		// client never composes it from an origin it knows: that would
		// freeze the page's location on the service's side forever, and a
		// client already in the field could not be told it had moved.
		// Without one the sentence still says what to do and where.
		if s.AuthorizeURL == "" {
			return "Complete your data authorization in the Trajector dashboard, then uploads resume."
		}
		return "Complete your data authorization at " + s.AuthorizeURL + " — then uploads resume."
	case RateLimited, TimedOut:
		return "Uploads resume automatically; `trajector upload --force` offers them now."
	default:
		return ""
	}
}

func (s Standing) pauseUntil() string { return s.NotBefore.UTC().Format(time.RFC3339) }

// versionStanding is the one derivation of whether the service's stated
// minimum client version stands against this build. Every surface reads
// the standing it returns; none of them compares versions.
//
// A minimum this build meets is not a standing at all: the service
// states it on every acknowledgement, not only when it matters, so
// relaying it verbatim left a compliant machine permanently told to
// upgrade — and a user who learns to skip those two lines skips them
// exactly when the service really does refuse.
//
// A pair no order covers is not the same as being behind: the
// requirement is stated and no remedy is offered. The service's own
// words about a refusal outrank both comparisons — they are cleared by
// the next acknowledgement, so while they stand uploads really are
// stopped, whatever the arithmetic says.
//
// refused is the service having just answered a refusal to the attempt
// in hand. It holds the gate even when the answer named neither a
// minimum nor a reason, and it makes the standing upgradable whatever
// the arithmetic says: the service refused this build a moment ago, so
// replacing it is the only move left. A minimum merely stated on disk
// is not that, which is why the comparison still governs there.
func versionStanding(refused bool, minClientVersion, message, version string) Standing {
	order, ordered := semver.Compare(version, minClientVersion)
	behind := ordered && order < 0
	s := Standing{Reason: VersionGate, Version: version, Message: message, Refused: refused, Upgradable: refused || behind || message != ""}
	if minClientVersion != "" && (!ordered || behind) {
		s.MinClientVersion = minClientVersion
	}
	if !refused && s.MinClientVersion == "" && message == "" {
		return Standing{}
	}
	return s
}
