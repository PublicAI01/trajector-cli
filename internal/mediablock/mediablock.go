// Package mediablock rewrites the payload of the image and document
// blocks a record carries, on the record's way into a batch. A payload
// is the base64 text in the source of such a block; nothing else in a
// record is touched. There are two rewrites:
//
//   - A repeated payload becomes a reference to a copy of it that the
//     service already holds, so each image in a session goes up once
//     instead of once per call that repeats it. A reference is exact:
//     putting the payload back gives the bytes the record held before.
//   - When the user turns image and document upload off, every payload
//     becomes a placeholder that says what was left out, never what it
//     showed.
//
// Both recording paths go through the same Pass, so the same block
// leaves this machine in the same shape whichever path read it.
//
// A block is an object whose "type" is exactly "image" or "document"
// and whose "source" is an object whose "type" is exactly "base64" and
// whose "data" is a non-empty string. The match is exact on purpose:
// "type" is free text a model or a tool writes into a record, and a
// prefix match would let anything calling itself image_metadata decide
// what this package rewrites.
//
// A session line holds the same content a second time when Claude
// Code's Read tool opened an image or a PDF: under "toolUseResult" at
// the root of the line, an object whose "type" is exactly "image" or
// "pdf" and whose "file" holds the content as a non-empty "base64"
// string. With upload turned off it becomes a placeholder too. It is
// never referred to: it appears once per line. Content a line holds in
// any other shape is not a block and leaves as it is — the images an
// older Claude Code kept from a notebook's cell outputs are one such
// copy; see PRIVACY.md.
//
// The shapes written here, and the digest they carry, are a contract
// with the receiving service: it must be able to tell an original, a
// reference, and a placeholder apart, and put a reference back.
package mediablock

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	// The decoders registered here are the image formats whose size a
	// placeholder can state. A format without one is still replaced; its
	// placeholder carries no width and height.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"strconv"
)

// The source types a rewritten block declares. An original keeps
// "base64".
const (
	// SourceReference is the source type of a payload replaced by a
	// reference to an earlier copy.
	SourceReference = "sha256_ref"
	// SourceOmitted is the source type of a payload replaced by a
	// placeholder.
	SourceOmitted = "omitted"
)

// Policy is what one batch does with the payloads its records carry.
// The zero Policy sends every payload as it is.
type Policy struct {
	// Omit replaces every payload with a placeholder.
	Omit bool
	// Sent reports whether the service acknowledged a batch that carried
	// the payload with this digest in full, in this scope. Nil means the
	// service has not said that it resolves references, so none is
	// written — not even to a copy carried earlier in the same batch.
	Sent func(scope, digest string) bool
}

// Original is a payload that a batch carries in full: the scope of the
// record that carried it and the payload's digest.
type Original struct {
	Scope  string
	Digest string
}

// Pass rewrites the records of one batch. It remembers the payloads
// that the records already packed carry in full, so a later record of
// the same scope can refer to them: the service stores a batch whole or
// not at all. A Pass is not reused across batches.
type Pass struct {
	policy    Policy
	carried   map[string]map[string]bool
	originals []Original
}

// NewPass starts the rewrite of one batch under policy.
func NewPass(policy Policy) *Pass {
	return &Pass{policy: policy, carried: map[string]map[string]bool{}}
}

// Rewritten is one record after the pass, and the payloads it still
// carries in full.
type Rewritten struct {
	Data      []byte
	scope     string
	originals []string
}

// Rewrite rewrites the payloads of one record. scope names the session
// the record belongs to, in a spelling that also names which recording
// path read it; an empty scope never gets a reference. data is JSON
// lines; a stored rawcall is one line. A line that is not exactly one
// JSON value is left as it is, and so is every block in it.
//
// The record is not yet part of the batch: Keep adds it, once the
// caller knows the record is packed.
func (p *Pass) Rewrite(scope string, data []byte) Rewritten {
	out := Rewritten{Data: data, scope: scope}
	// A record that spells "base64" nowhere, not even with an escape,
	// holds no block: the walk below is skipped for it.
	if !bytes.Contains(data, []byte("base64")) && !bytes.Contains(data, []byte(`\u`)) {
		return out
	}
	blocks := findBlocks(data)
	if len(blocks) == 0 {
		return out
	}
	kept := map[string]bool{}
	var edits []edit
	for _, b := range blocks {
		digest := b.digest()
		switch {
		case p.policy.Omit:
			edits = append(edits, b.placeholder(digest)...)
		case b.readResult:
		case b.exact && p.refers(scope, digest, kept):
			edits = append(edits, b.reference(digest)...)
		default:
			if !kept[digest] {
				kept[digest] = true
				out.originals = append(out.originals, digest)
			}
		}
	}
	out.Data = apply(data, edits)
	return out
}

// refers reports whether a copy of this payload may be sent as a
// reference: the service resolves references, and it holds the
// payload — from an acknowledged batch, from a record already packed in
// this one, or from earlier in this same record.
func (p *Pass) refers(scope, digest string, kept map[string]bool) bool {
	if scope == "" || p.policy.Sent == nil {
		return false
	}
	return kept[digest] || p.carried[scope][digest] || p.policy.Sent(scope, digest)
}

// Keep adds a rewritten record to the batch: the payloads it carries in
// full may be referred to by the records packed after it. A record with
// an empty scope adds nothing, since no reference can point at it.
func (p *Pass) Keep(r Rewritten) {
	if r.scope == "" || len(r.originals) == 0 {
		return
	}
	carried := p.carried[r.scope]
	if carried == nil {
		carried = map[string]bool{}
		p.carried[r.scope] = carried
	}
	for _, digest := range r.originals {
		if carried[digest] {
			continue
		}
		carried[digest] = true
		p.originals = append(p.originals, Original{Scope: r.scope, Digest: digest})
	}
}

// Originals lists the payloads the kept records carry in full, once per
// scope and digest.
func (p *Pass) Originals() []Original {
	return slices.Clone(p.originals)
}

// span is the half-open byte range of one JSON token in a record.
type span struct{ start, end int }

// block is one image or document block found in a record: the three
// tokens a rewrite replaces, and the payload.
type block struct {
	sourceType span
	dataKey    span
	dataValue  span
	// payload is the decoded value of the data string: the base64 text.
	payload []byte
	// exact reports that none of the three tokens holds an escape and
	// the source has no sha256 member, so putting a reference back
	// restores the record byte for byte.
	exact bool
	// displaced are the members, with their separators, that a
	// placeholder removes because it writes a member of the same name.
	displaced []span
	// readResult marks the content a Read result keeps on a session
	// line. It has no source type to replace, it appears once per line,
	// and it is only ever omitted: never referred to, and never an
	// original another copy refers to.
	readResult bool
}

// digest is the lowercase hex SHA-256 of the base64 text, not of the
// bytes it encodes. The text is what a reference restores, and base64
// has more than one spelling of the same bytes.
func (b block) digest() string {
	sum := sha256.Sum256(b.payload)
	return hex.EncodeToString(sum[:])
}

// reference replaces the payload with its digest. Only the three
// tokens change, so the key order and every other byte of the block
// stay as observed, and the reverse edit restores them.
func (b block) reference(digest string) []edit {
	return []edit{
		{span: b.sourceType, text: strconv.Quote(SourceReference)},
		{span: b.dataKey, text: `"sha256"`},
		{span: b.dataValue, text: `"` + digest + `"`},
	}
}

// placeholder replaces the payload with its digest and what can be read
// off it without showing it: the size of the decoded payload, and the
// width and height an image states in its header. A value that cannot
// be read is left out, never guessed. A member already named like one
// of these is removed, even when the placeholder cannot state its own:
// the names belong to the placeholder, and a reader would otherwise
// take the record's value for one read off the payload.
func (b block) placeholder(digest string) []edit {
	text := `"` + digest + `"`
	if decoded, err := base64.StdEncoding.DecodeString(string(b.payload)); err == nil {
		text += `,"bytes":` + strconv.Itoa(len(decoded))
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded)); err == nil {
			text += `,"width":` + strconv.Itoa(cfg.Width) + `,"height":` + strconv.Itoa(cfg.Height)
		}
	}
	edits := []edit{
		{span: b.dataKey, text: `"sha256"`},
		{span: b.dataValue, text: text},
	}
	for _, s := range b.displaced {
		edits = append(edits, edit{span: s})
	}
	if b.readResult {
		return edits
	}
	return append(edits, edit{span: b.sourceType, text: strconv.Quote(SourceOmitted)})
}

// edit replaces one token of a record.
type edit struct {
	span
	text string
}

// apply returns data with every edit made. Edits replace one token each,
// and no token belongs to two blocks. The one overlap is a member a
// placeholder removes that holds a block of its own: the edits inside
// the removed range go with it.
func apply(data []byte, edits []edit) []byte {
	if len(edits) == 0 {
		return data
	}
	slices.SortFunc(edits, func(a, b edit) int { return cmp.Compare(a.start, b.start) })
	out := make([]byte, 0, len(data))
	prev := 0
	for _, e := range edits {
		if e.start < prev {
			continue
		}
		out = append(out, data[prev:e.start]...)
		out = append(out, e.text...)
		prev = e.end
	}
	return append(out, data[prev:]...)
}
