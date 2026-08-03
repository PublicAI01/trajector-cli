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
type FlushReply struct {
	Service string  `json:"service"`
	Outcome Outcome `json:"outcome"`
	Batches int     `json:"batches"`
	Records int     `json:"records"`
	Error   string  `json:"error,omitempty"`
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
			Service: serviceName,
			Outcome: result.Outcome,
			Batches: result.Batches,
			Records: result.Records,
		}
		if err != nil {
			reply.Error = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reply)
	})
}
