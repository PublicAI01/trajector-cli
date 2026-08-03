// Package envelope defines the schema_version 1 rawcall record: the
// on-disk envelope wrapping one captured API call. It is the only place
// that decides what an observed exchange looks like once stored, and the
// only place that reads a stored record back. The serialized layout is a
// documented product contract; changing field names or semantics
// requires a new schema version.
package envelope

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	schemaVersion = "1"
	sourceProxy   = "proxy"

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

	// Assemble reassembles a complete event stream into the equivalent
	// non-streaming object, or fails so the raw stream is kept verbatim.
	// AssemblyRules names the rules it applied.
	Assemble      func(stream []byte) ([]byte, error)
	AssemblyRules string

	// UpstreamRequestID is the id the upstream reported for this
	// exchange, used when the response body carries none.
	UpstreamRequestID string

	Hints FormatHints
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
	HTTPStatus     int          `json:"http_status"`
	ClientVersion  string       `json:"client_version"`
	Timestamp      string       `json:"timestamp"`
	ProjectIDHash  string       `json:"project_id_hash"`
	UpstreamOrigin string       `json:"upstream_origin"`
	SSEAssembly    wireAssembly `json:"sse_assembly"`
	Garbled        bool         `json:"garbled"`
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
		SchemaVersion: schemaVersion,
		Source:        sourceProxy,
		Provider:      obs.Provider,
		Endpoint:      obs.Endpoint,
		RequestID:     obs.requestID(response, structured),
		Capture: wireCapture{
			HTTPStatus:     obs.HTTPStatus,
			ClientVersion:  obs.ClientVersion,
			Timestamp:      obs.At.UTC().Format(time.RFC3339Nano),
			ProjectIDHash:  obs.ProjectIDHash,
			UpstreamOrigin: Origin(obs.Upstream, obs.OfficialUpstream),
			SSEAssembly:    assembly,
			Garbled:        garbled || requestGarbled,
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
	if rec.SchemaVersion != schemaVersion {
		return Envelope{}, fmt.Errorf("envelope: unsupported schema version %q", rec.SchemaVersion)
	}
	return Envelope{data: append([]byte(nil), data...), rec: rec}, nil
}

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
		if obs.ResponseComplete && obs.Assemble != nil {
			if assembled, err := obs.Assemble(obs.Response); err == nil {
				assembly.By = assembledByClient
				assembly.RulesVersion = obs.AssemblyRules
				return json.RawMessage(assembled), assembly, true, false
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
