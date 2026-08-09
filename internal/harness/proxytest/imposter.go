package proxytest

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
)

// Imposter is a real TCP listener squatting a proxy address and
// answering with a copy of a proxy's health payload — everything a
// process that cannot read the owning user's files can do. With
// ProveAfter it instead stands in for the user's own proxy caught
// before its token publication. It records what it receives, so a test
// can assert that no credential ever reached it.
type Imposter struct {
	addr string

	mu       sync.Mutex
	proof    string
	token    string
	unproven int
	seen     []imposterRequest
	carried  map[net.Conn]bool
}

type imposterRequest struct {
	method string
	path   string
	header http.Header
}

// StartImposter squats a fresh loopback address, answering every GET
// with health as its body and every POST with 202 Accepted, the same
// shapes a live proxy serves. It is stopped with the test.
func StartImposter(t *testing.T, health Health) *Imposter {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	im := &Imposter{addr: l.Addr().String(), carried: map[net.Conn]bool{}}
	srv := &http.Server{ConnState: im.noteConn, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		im.mu.Lock()
		im.seen = append(im.seen, imposterRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone()})
		proof := im.proof
		if nonce := r.Header.Get(apiproxy.ChallengeHeader); nonce != "" && im.token != "" {
			if im.unproven > 0 {
				im.unproven--
			} else {
				proof = apiproxy.Proof(im.token, nonce, r.Host)
			}
		}
		im.mu.Unlock()
		if proof != "" {
			w.Header().Set(apiproxy.ProofHeader, proof)
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return im
}

// Addr is the squatted address.
func (im *Imposter) Addr() string { return im.addr }

// ReplayProof makes the imposter answer every challenge with a fixed,
// previously collected proof — the strongest answer available to a
// process that can challenge a real proxy itself but cannot read its
// token.
func (im *Imposter) ReplayProof(proof string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.proof = proof
}

// ProveAfter makes the imposter answer challenges with valid proofs
// computed from token once n challenges have gone unproven — the way a
// holder caught between winning its bind and publishing its admin
// token answers before and after the publication lands.
func (im *Imposter) ProveAfter(n int, token string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.unproven = n
	im.token = token
}

// noteConn records a connection the moment it carries a request, so a
// bare liveness dial — which carries none — is not counted as one.
func (im *Imposter) noteConn(c net.Conn, state http.ConnState) {
	if state != http.StateActive {
		return
	}
	im.mu.Lock()
	defer im.mu.Unlock()
	im.carried[c] = true
}

// Connections reports how many distinct connections carried a request,
// so a test can tell requests that each opened their own connection
// from requests that rode one pooled connection.
func (im *Imposter) Connections() int {
	im.mu.Lock()
	defer im.mu.Unlock()
	return len(im.carried)
}

// Requests reports how many requests the imposter received.
func (im *Imposter) Requests() int {
	im.mu.Lock()
	defer im.mu.Unlock()
	return len(im.seen)
}

// Saw reports whether any received request matched method and path.
func (im *Imposter) Saw(method, path string) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	for _, r := range im.seen {
		if r.method == method && r.path == path {
			return true
		}
	}
	return false
}

// SawHeader reports whether any received request carried a nonempty
// value for the named header.
func (im *Imposter) SawHeader(name string) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	for _, r := range im.seen {
		if r.header.Get(name) != "" {
			return true
		}
	}
	return false
}
