package upload

import (
	"encoding/json"
	"net/http"
)

// FlushPath asks the resident uploader to run one flush and report the
// result. The uploader lives in the proxy process — the machine's one
// flusher — so the CLI triggers uploads there instead of running its
// own.
const FlushPath = "/trajector/flush"

// FlushReply is what the proxy reports about one requested flush. The
// handler that writes it and the CLI that reads it share this type, so
// the wire contract is checked by the compiler rather than by two
// hand-written codecs agreeing.
//
// Outcome is authoritative and Error is its detail: a classified flush
// carries the same outcome whether or not an error came with it, so a
// reader switches on Outcome first and falls back to Error only when
// the outcome does not decide.
type FlushReply struct {
	Service string  `json:"service"`
	Outcome Outcome `json:"outcome"`
	Batches int     `json:"batches"`
	Records int     `json:"records"`
	// SetAside lists the rejections this flush wrote for rawcalls that no
	// longer read back as rawcalls, so the caller can say so — with the
	// cause — instead of the records going quiet.
	SetAside []Rejection `json:"set_aside,omitempty"`
	// MinClientVersion is set when the service gates this client
	// version, so the caller can name the required version without
	// reading the handshake file across processes.
	MinClientVersion string `json:"min_client_version,omitempty"`
	// UpgradeMessage carries the service's own words about the refusal,
	// for the same reason: the caller relays them without reaching
	// across processes for the handshake file.
	UpgradeMessage string `json:"upgrade_message,omitempty"`
	// AuthorizeURL and AuthorizationMessage carry, for an
	// AuthorizationRequired outcome, where the user completes the
	// authorization and what the service said about it — again so the
	// caller relays them without reaching for the handshake file.
	AuthorizeURL         string `json:"authorize_url,omitempty"`
	AuthorizationMessage string `json:"authorization_message,omitempty"`
	Error                string `json:"error,omitempty"`
}

// Handler serves the flush endpoint, for the composition root to mount
// under the proxy's reserved prefix. serviceName identifies the proxy
// in replies. A flush runs synchronously — the caller wants the
// outcome, and the uploader itself serializes concurrent requests.
func (u *Uploader) Handler(serviceName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != FlushPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		result, err := u.Flush(r.URL.Query().Get("force") == "1")
		reply := FlushReply{
			Service:          serviceName,
			Outcome:          result.Outcome,
			Batches:          result.Batches,
			Records:          result.Records,
			SetAside:         result.SetAside,
			MinClientVersion: result.MinClientVersion,
			UpgradeMessage:   result.UpgradeMessage,

			AuthorizeURL:         result.AuthorizeURL,
			AuthorizationMessage: result.AuthorizationMessage,
		}
		if err != nil {
			reply.Error = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reply)
	})
}
