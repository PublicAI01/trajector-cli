// Package envelope defines the records this client stores and uploads:
// the rawcall wrapping one captured API call, the segments and
// snapshots read from what Claude Code wrote, and the git snapshot one
// hook observed. It is the only place that decides what an observation
// looks like once stored, and the only place that reads a stored record
// back. The serialized layout is a documented product contract;
// changing field names or semantics requires a new schema version.
package envelope

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the one version number a batch and every record it
// carries declare. There is no second version axis: a batch of this
// version holds records of this version only, which is why the constant
// is stated here once and read by every package that writes a version.
const SchemaVersion = "3"

// readableVersions are the versions this client still reads back. Only
// SchemaVersion is ever written; the older values are readable so a
// record stored before an upgrade is packed and uploaded rather than
// refused, and they are restated to the current version on the way into
// a batch.
var readableVersions = map[string]bool{"1": true, "2": true, SchemaVersion: true}

// checkVersion reports whether a stored record declares a version this
// client can read.
func checkVersion(version string) error {
	if !readableVersions[version] {
		return fmt.Errorf("envelope: unsupported schema version %q", version)
	}
	return nil
}

const (
	sourceProxy = "proxy"

	// Upstream origin values. The origin must be recorded truthfully
	// and never defaulted to official: a rawcall that flowed through a
	// user-configured third-party upstream must say so.
	originOfficial   = "official"
	originThirdParty = "third_party"

	// SSE assembly responsibility. "client" means the response field
	// holds the reassembled non-streaming object; "none" means no
	// assembly was performed (non-streaming response, or degraded raw
	// SSE text).
	assembledByClient = "client"
	assembledByNone   = "none"

	// localRequestIDPrefix marks request ids generated on this machine
	// because the exchange carried none.
	localRequestIDPrefix = "local-"
)

// storableRequestID is the id shape this package is willing to emit.
// Anything else is replaced by a locally generated id, so a stored
// record can never name itself out of its own directory.
var storableRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidRequestID reports whether id is safe to appear in a stored
// rawcall file name. The envelope emitting an id and the spool building
// a path from it share this one definition, so an id can never escape
// its day directory.
func ValidRequestID(id string) bool { return storableRequestID.MatchString(id) }

// FormatHints carries the provider's self-declared format signals.
// Credential headers must never be added here: hints are copied field
// by field, never from the full header set.
type FormatHints struct {
	AnthropicVersion string   `json:"anthropic-version"`
	AnthropicBeta    []string `json:"anthropic-beta,omitempty"`
}

// Observation is everything one proxied exchange revealed. Callers
// report what they saw; every judgment about what those bytes mean is
// made here.
type Observation struct {
	Provider      string
	Endpoint      string
	HTTPStatus    int
	ClientVersion string
	ProjectIDHash string
	// At is the capture time. Zero means now.
	At time.Time

	// Upstream answered this exchange; OfficialUpstream is the
	// provider's own origin.
	Upstream         string
	OfficialUpstream string

	// Request and Response are the exact bytes exchanged, and the
	// Complete flags report whether each side was read to its end. A
	// truncated body is still stored — data is never dropped — but the
	// record is marked garbled.
	Request          []byte
	RequestComplete  bool
	Response         []byte
	ResponseComplete bool

	// ContentType and ContentEncoding are the upstream's own response
	// headers. A non-empty encoding means these bytes are not the plain
	// payload and cannot be interpreted further.
	ContentType     string
	ContentEncoding string

	// Assembler reassembles a complete event stream, or fails so the raw
	// stream is kept verbatim. The zero value performs no assembly.
	Assembler Assembler

	// UpstreamRequestID is the id the upstream reported for this
	// exchange. It is stored whenever it was observed, and it also
	// names the record when the response body carries no id of its own.
	UpstreamRequestID string

	Hints FormatHints
}

// Assembler reassembles a complete event stream under named rules. The
// function and the rules version travel as one value, so a record can
// never claim rules that were not applied.
type Assembler struct {
	// Rules names the reassembly rules Assemble applies.
	Rules string
	// Assemble returns the equivalent non-streaming object, or fails so
	// the raw stream is stored verbatim instead.
	Assemble func(stream []byte) (json.RawMessage, error)
}

// wire is the serialized rawcall. Every JSON tag here is part of the
// documented contract.
type wire struct {
	SchemaVersion string          `json:"schema_version"`
	Source        string          `json:"source"`
	Provider      string          `json:"provider"`
	Endpoint      string          `json:"endpoint"`
	RequestID     string          `json:"request_id"`
	Capture       wireCapture     `json:"capture"`
	FormatHints   FormatHints     `json:"format_hints"`
	Request       json.RawMessage `json:"request"`
	Response      json.RawMessage `json:"response"`
}

// wireCapture describes how the rawcall was observed. All values are
// copies of observed facts and are never rewritten downstream.
type wireCapture struct {
	HTTPStatus    int    `json:"http_status"`
	ClientVersion string `json:"client_version"`
	Timestamp     string `json:"timestamp"`
	ProjectIDHash string `json:"project_id_hash"`
	// UpstreamRequestID is absent, never a placeholder, when the
	// upstream reported no id.
	UpstreamRequestID string       `json:"upstream_request_id,omitempty"`
	UpstreamOrigin    string       `json:"upstream_origin"`
	SSEAssembly       wireAssembly `json:"sse_assembly"`
	Garbled           bool         `json:"garbled"`
}

// wireAssembly records which side reassembled the event stream and under
// which rules, so degraded records can be reassembled later.
type wireAssembly struct {
	By            string `json:"by"`
	ClientVersion string `json:"client_version"`
	RulesVersion  string `json:"rules_version"`
}

// Envelope is one rawcall record: its serialized bytes plus read access
// to the facts they carry.
type Envelope struct {
	data []byte
	rec  wire
}

// Record classifies an observation and serializes it as a rawcall.
func Record(obs Observation) (Envelope, error) {
	if obs.At.IsZero() {
		obs.At = time.Now()
	}
	response, assembly, structured, garbled := obs.classifyResponse()
	request, requestGarbled := obs.classifyRequest()

	rec := wire{
		SchemaVersion: SchemaVersion,
		Source:        sourceProxy,
		Provider:      obs.Provider,
		Endpoint:      obs.Endpoint,
		RequestID:     obs.requestID(response, structured),
		Capture: wireCapture{
			HTTPStatus:        obs.HTTPStatus,
			ClientVersion:     obs.ClientVersion,
			Timestamp:         obs.At.UTC().Format(time.RFC3339Nano),
			ProjectIDHash:     obs.ProjectIDHash,
			UpstreamRequestID: obs.UpstreamRequestID,
			UpstreamOrigin:    Origin(obs.Upstream, obs.OfficialUpstream),
			SSEAssembly:       assembly,
			Garbled:           garbled || requestGarbled,
		},
		FormatHints: obs.Hints,
		Request:     request,
		Response:    response,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return Envelope{}, fmt.Errorf("envelope: serializing rawcall: %w", err)
	}
	return Envelope{data: data, rec: rec}, nil
}

// Parse reads a stored rawcall back.
func Parse(data []byte) (Envelope, error) {
	var rec wire
	if err := json.Unmarshal(data, &rec); err != nil {
		return Envelope{}, fmt.Errorf("envelope: reading rawcall: %w", err)
	}
	if err := checkVersion(rec.SchemaVersion); err != nil {
		return Envelope{}, err
	}
	return Envelope{data: append([]byte(nil), data...), rec: rec}, nil
}

// Restated returns the record's bytes with its schema version set to
// the current one and every other byte as it was. A batch and the
// records in it declare one version, so a rawcall stored under an
// earlier one is restated on its way into a batch. A record already at
// the current version is returned as it was stored, so the common path
// copies nothing.
//
// The version token is replaced where it stands rather than the record
// being serialized again: the bodies a rawcall carries are observed
// bytes, and a round trip through the encoder would re-escape them.
func (e Envelope) Restated() ([]byte, error) {
	if e.rec.SchemaVersion == SchemaVersion {
		return e.data, nil
	}
	return restateVersion(e.data)
}

// versionPrefix is how every record this package writes begins: the
// schema version is the first member, which is what lets restating it
// be one replacement instead of a re-serialization.
const versionPrefix = `{"schema_version":"`

func restateVersion(data []byte) ([]byte, error) {
	rest, ok := bytes.CutPrefix(data, []byte(versionPrefix))
	if !ok {
		return nil, errNoVersionToken
	}
	_, tail, ok := bytes.Cut(rest, []byte(`"`))
	if !ok {
		return nil, errNoVersionToken
	}
	out := make([]byte, 0, len(data))
	out = append(out, versionPrefix...)
	out = append(out, SchemaVersion...)
	out = append(out, '"')
	return append(out, tail...), nil
}

var errNoVersionToken = errors.New("envelope: record does not open with its schema version")

// ProjectIDHashOf reads only which project a stored rawcall belongs to.
// It deliberately validates nothing else: consent withdrawal must be
// able to find and delete a project's records even among records it
// could not otherwise interpret.
func ProjectIDHashOf(data []byte) (string, bool) {
	var rec struct {
		Capture struct {
			ProjectIDHash string `json:"project_id_hash"`
		} `json:"capture"`
	}
	if err := json.Unmarshal(data, &rec); err != nil || rec.Capture.ProjectIDHash == "" {
		return "", false
	}
	return rec.Capture.ProjectIDHash, true
}

// Origin classifies which upstream served an exchange.
func Origin(upstream, official string) string {
	if trimSlash(upstream) == trimSlash(official) {
		return originOfficial
	}
	return originThirdParty
}

// Bytes returns the serialized record.
func (e Envelope) Bytes() []byte { return e.data }

// RequestID identifies this rawcall and names its file in the spool.
func (e Envelope) RequestID() string { return e.rec.RequestID }

// SessionKey copies the request's own session identity verbatim. The
// client attaches it for adjacency when batching and interprets it no
// further. It is empty when the request carried no session identity.
func (e Envelope) SessionKey() string {
	var v struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(e.rec.Request, &v) != nil {
		return ""
	}
	return v.Metadata.UserID
}

// Timestamp is when the exchange was captured. A record whose timestamp
// cannot be read reports the zero time rather than a guess.
func (e Envelope) Timestamp() time.Time {
	t, err := time.Parse(time.RFC3339Nano, e.rec.Capture.Timestamp)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ProjectIDHash is the consenting project this rawcall came from.
func (e Envelope) ProjectIDHash() string { return e.rec.Capture.ProjectIDHash }

// Endpoint is the request path that was captured.
func (e Envelope) Endpoint() string { return e.rec.Endpoint }

// HTTPStatus is the status the upstream returned.
func (e Envelope) HTTPStatus() int { return e.rec.Capture.HTTPStatus }

// UpstreamRequestID is the id the upstream reported for the exchange,
// or empty when it reported none.
func (e Envelope) UpstreamRequestID() string { return e.rec.Capture.UpstreamRequestID }

// UpstreamOrigin reports whether the provider's own API or a
// user-configured third-party upstream served the exchange.
func (e Envelope) UpstreamOrigin() string { return e.rec.Capture.UpstreamOrigin }

// Garbled reports that at least one body could not be stored as
// structured JSON and was kept as raw text instead.
func (e Envelope) Garbled() bool { return e.rec.Capture.Garbled }

// AssembledBy reports which side reassembled a streamed response.
func (e Envelope) AssembledBy() string { return e.rec.Capture.SSEAssembly.By }

// Request is the stored request body.
func (e Envelope) Request() []byte { return e.rec.Request }

// Response is the stored response body.
func (e Envelope) Response() []byte { return e.rec.Response }

// Hints are the provider's self-declared format signals.
func (e Envelope) Hints() FormatHints { return e.rec.FormatHints }

// classifyResponse decides how the response bytes are stored. Data is
// never dropped: whatever cannot be represented faithfully is kept as
// raw text and marked garbled, and observed values are never rewritten.
func (obs Observation) classifyResponse() (body json.RawMessage, assembly wireAssembly, structured, garbled bool) {
	assembly = wireAssembly{By: assembledByNone, ClientVersion: obs.ClientVersion}
	switch {
	case obs.ContentEncoding != "":
		// An encoding the transport could not transparently decode.
		return jsonString(obs.Response), assembly, false, true
	case strings.HasPrefix(obs.ContentType, "text/event-stream"):
		if obs.ResponseComplete && obs.Assembler.Assemble != nil {
			if assembled, err := obs.Assembler.Assemble(obs.Response); err == nil {
				assembly.By = assembledByClient
				assembly.RulesVersion = obs.Assembler.Rules
				return assembled, assembly, true, false
			}
		}
		return jsonString(obs.Response), assembly, false, true
	case obs.ResponseComplete && json.Valid(obs.Response):
		return json.RawMessage(obs.Response), assembly, true, false
	default:
		return jsonString(obs.Response), assembly, false, true
	}
}

func (obs Observation) classifyRequest() (body json.RawMessage, garbled bool) {
	if obs.RequestComplete && json.Valid(obs.Request) {
		return json.RawMessage(obs.Request), false
	}
	return jsonString(obs.Request), true
}

// requestID prefers the response's own id, falls back to the id the
// upstream reported, and generates a marked local id when neither is
// usable.
func (obs Observation) requestID(response json.RawMessage, structured bool) string {
	id := ""
	if structured {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(response, &v) == nil {
			id = v.ID
		}
	}
	if id == "" {
		id = obs.UpstreamRequestID
	}
	if !storableRequestID.MatchString(id) {
		id = newLocalRequestID(obs.At)
	}
	return id
}

// newLocalRequestID never fails: a capture must not be lost because the
// CSPRNG is unavailable, so the capture time is the fallback source of
// uniqueness.
func newLocalRequestID(at time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		binary.BigEndian.PutUint64(b[:8], uint64(at.UnixNano()))
	}
	return localRequestIDPrefix + hex.EncodeToString(b[:])
}

// jsonString encodes a body that is not valid JSON (raw SSE text, an
// undecodable encoding, a truncated or non-JSON payload) as a JSON
// string so it can sit in the request or response slot without breaking
// the record's own structure.
func jsonString(body []byte) json.RawMessage {
	encoded, err := json.Marshal(string(body))
	if err != nil {
		// Marshaling a string cannot fail; invalid UTF-8 is replaced.
		return json.RawMessage(`""`)
	}
	return encoded
}

func trimSlash(u string) string {
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}
