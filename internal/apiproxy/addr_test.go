package apiproxy_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
)

// The proxy binds loopback only. The address reaches the bind from
// `proxy serve --addr` and from an environment variable a repository's
// committed Claude Code settings can set on a session hook, so an
// address naming anything but this machine has to be refused rather
// than bound. 2026-08-15.
func TestValidateAddrRefusesAnythingButLoopback(t *testing.T) {
	for _, addr := range []string{
		"0.0.0.0:41100",
		":41100",
		"192.168.1.5:41100",
		"10.0.0.7:41100",
		"[::]:41100",
		"example.com:41100",
		"127.0.0.1",
		"",
	} {
		t.Run(addr, func(t *testing.T) {
			err := apiproxy.ValidateAddr(addr)
			if err == nil {
				t.Fatalf("ValidateAddr(%q) = nil, want a refusal: binding it exposes the proxy beyond this machine", addr)
			}
			if !strings.Contains(err.Error(), addr) {
				t.Errorf("err = %v, want it to name the address it refused", err)
			}
		})
	}
}

func TestValidateAddrAcceptsEveryLoopbackSpelling(t *testing.T) {
	for _, addr := range []string{
		apiproxy.Addr,
		"127.0.0.1:0",
		"127.0.0.2:41100",
		"localhost:41100",
		"[::1]:41100",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := apiproxy.ValidateAddr(addr); err != nil {
				t.Fatalf("ValidateAddr(%q) = %v, want it accepted", addr, err)
			}
		})
	}
}
