// Command probelistener is the capture side of the Claude Code
// injection probe (scripts/claude_injection_probe.sh). It listens on an
// ephemeral loopback port, appends one line per received request to a
// log file, and answers a non-retryable API error so the probed client
// fails fast without reaching any real upstream. No request body is
// read or stored: the probe only needs to know which port was hit.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	logPath := flag.String("log", "", "file receiving one line per request")
	flag.Parse()
	if *logPath == "" {
		log.Fatal("probelistener: -log is required")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("probelistener: %v", err)
	}
	// The shell script scrapes this line for the address.
	fmt.Printf("LISTENING %s\n", l.Addr())

	err = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%s %s\n", r.Method, r.URL.Path)
			f.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		// 400 is not retried by Anthropic SDKs, so one probe request
		// stays one log line.
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"injection probe endpoint; no request is ever forwarded"}}`)
	}))
	log.Fatal(err)
}
