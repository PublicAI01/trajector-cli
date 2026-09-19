package envelope

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Kind is what a stored record declares itself to be, read before
// anything else about it is interpreted. RecordKind is empty for a
// rawcall.
type Kind struct {
	Source     string
	RecordKind string
}

// Kind values of the records this package can read.
var (
	KindRawcall      = Kind{Source: sourceProxy}
	KindSegment      = Kind{Source: sourceTranscript, RecordKind: kindSegment}
	KindMetaSnapshot = Kind{Source: sourceTranscript, RecordKind: kindMetaSnapshot}
	KindGitSnapshot  = Kind{Source: sourceHook, RecordKind: kindGitSnapshot}
)

// RecordHeader is what a stored record declares about itself before any
// field of its own kind is read: which kind it is, the id it is
// addressed by, which coding session it belongs to, and what was
// observed when it was captured. A rawcall declares the last three
// under other names — it carries its session inside the request it
// wrapped — so a rawcall's header carries its id alone and Parse reads
// the rest.
type RecordHeader struct {
	Kind      Kind
	RecordID  string
	SessionID string
	Capture   Capture
}

// kindSpec is one row of the kind table: what the rest of this client
// needs in order to count, store and dispatch a record without naming
// its kind.
type kindSpec struct {
	kind Kind
	// countKey is the name a count of this kind travels under in the
	// diagnostics this client writes. It is a product contract: the set
	// of keys may grow, but a key already written is never respelled.
	countKey string
	// read confirms bytes that declare this kind still parse as it, and
	// returns what they declare.
	read func(data []byte) (RecordHeader, error)
}

// kinds is the one declaration of the record kinds this client stores,
// in the order a count or a listing presents them. A kind added here is
// counted, stored, addressed and dispatched with no edit elsewhere;
// only the user's word for it lives outside this package, because that
// word is the user's and not the wire's.
var kinds = []kindSpec{
	{
		kind:     KindRawcall,
		countKey: "rawcalls",
		read: func(data []byte) (RecordHeader, error) {
			env, err := Parse(data)
			if err != nil {
				return RecordHeader{}, err
			}
			return RecordHeader{Kind: KindRawcall, RecordID: env.RequestID()}, nil
		},
	},
	{
		kind:     KindSegment,
		countKey: "segments",
		read: func(data []byte) (RecordHeader, error) {
			seg, err := ParseSegment(data)
			if err != nil {
				return RecordHeader{}, err
			}
			return RecordHeader{Kind: KindSegment, RecordID: seg.RecordID, SessionID: seg.SessionID, Capture: seg.Capture}, nil
		},
	},
	{
		kind:     KindMetaSnapshot,
		countKey: "snapshots",
		read: func(data []byte) (RecordHeader, error) {
			snap, err := ParseMetaSnapshot(data)
			if err != nil {
				return RecordHeader{}, err
			}
			return RecordHeader{Kind: KindMetaSnapshot, RecordID: snap.RecordID, SessionID: snap.SessionID, Capture: snap.Capture}, nil
		},
	},
	{
		kind:     KindGitSnapshot,
		countKey: "git_snapshots",
		read: func(data []byte) (RecordHeader, error) {
			snap, err := ParseGitSnapshot(data)
			if err != nil {
				return RecordHeader{}, err
			}
			return RecordHeader{Kind: KindGitSnapshot, RecordID: snap.RecordID, SessionID: snap.SessionID, Capture: snap.Capture}, nil
		},
	},
}

func specOf(kind Kind) (kindSpec, bool) {
	i := slices.IndexFunc(kinds, func(s kindSpec) bool { return s.kind == kind })
	if i < 0 {
		return kindSpec{}, false
	}
	return kinds[i], true
}

// Kinds lists the record kinds this client stores, in the order counts
// and listings present them. The result is a copy, so no caller can
// change what the kinds are.
func Kinds() []Kind {
	out := make([]Kind, len(kinds))
	for i, spec := range kinds {
		out[i] = spec.kind
	}
	return out
}

// CountKey is the name a count of this kind travels under. It is empty
// for a kind this client does not store, so such a kind reaches no
// count and no report rather than one under a name nothing declared.
func (k Kind) CountKey() string {
	spec, ok := specOf(k)
	if !ok {
		return ""
	}
	return spec.countKey
}

// KindOf reads only a record's self-declaration, so a caller can pick
// the parser before committing to a layout.
func KindOf(data []byte) (Kind, error) {
	var k struct {
		Source     string `json:"source"`
		RecordKind string `json:"record_kind"`
	}
	if err := json.Unmarshal(data, &k); err != nil {
		return Kind{}, fmt.Errorf("envelope: reading record kind: %w", err)
	}
	return Kind{Source: k.Source, RecordKind: k.RecordKind}, nil
}

// ReadHeader reads what a record declares about itself and confirms the
// bytes still read back as the kind they name. A record that declares a
// kind this client does not store, or that no longer parses as the kind
// it claims, is refused here: nothing may interpret such a record.
// ProjectIDHashOf and SessionIDOf still address it, because withdrawal
// and deletion must reach a record this client can no longer read.
func ReadHeader(data []byte) (RecordHeader, error) {
	kind, err := KindOf(data)
	if err != nil {
		return RecordHeader{}, err
	}
	spec, ok := specOf(kind)
	if !ok {
		return RecordHeader{}, fmt.Errorf("envelope: record declares %s/%s, which is not a kind this client stores", kind.Source, kind.RecordKind)
	}
	return spec.read(data)
}

// SessionIDOf reads only which coding session a stored record belongs
// to, the way ProjectIDHashOf reads the project. It names no kind and
// validates nothing else: deleting a session's data must reach a record
// this client could not otherwise interpret. A rawcall declares no
// session under this name.
func SessionIDOf(data []byte) (string, bool) {
	var rec struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &rec); err != nil || rec.SessionID == "" {
		return "", false
	}
	return rec.SessionID, true
}
