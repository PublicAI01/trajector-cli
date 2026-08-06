package platform_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

func client(t *testing.T) (*platform.Client, *fakeplatform.Server) {
	t.Helper()
	server := fakeplatform.New(t)
	return platform.New(server.URL(), "test"), server
}

func TestStartPairingSendsClientVersion(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/pairings", fakeplatform.JSON(200, map[string]any{
		"pairing_id":       "pair-1",
		"verification_url": "https://example.com/pair",
		"user_code":        "ABCD-1234",
		"poll_interval_ms": 10,
	}))

	p, err := c.StartPairing("1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if p.PairingID != "pair-1" || p.UserCode != "ABCD-1234" {
		t.Errorf("pairing = %+v", p)
	}

	reqs := server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	var body map[string]string
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["client_version"] != "1.2.3" {
		t.Errorf("body = %v", body)
	}
}

func TestStartPairingRejectsIncompleteResponse(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/pairings", fakeplatform.JSON(200, map[string]any{"pairing_id": "pair-1"}))
	if _, err := c.StartPairing("1.2.3"); err == nil {
		t.Error("incomplete pairing response accepted")
	}
}

func TestPollPairingStates(t *testing.T) {
	c, server := client(t)
	server.Stub("GET", "/v1/pairings/pair-1", fakeplatform.JSON(200, map[string]any{"status": "pending"}))
	server.Stub("GET", "/v1/pairings/pair-1", fakeplatform.JSON(200, map[string]any{
		"status":       "paired",
		"device_token": "dev-tok-fake",
	}))

	first, err := c.PollPairing("pair-1")
	if err != nil || first.Status != platform.PairingPending {
		t.Fatalf("first poll = %+v, %v", first, err)
	}
	second, err := c.PollPairing("pair-1")
	if err != nil || second.Status != platform.PairingPaired || second.DeviceToken != "dev-tok-fake" {
		t.Fatalf("second poll = %+v, %v", second, err)
	}
}

func TestPollPairingRejectsPairedWithoutToken(t *testing.T) {
	c, server := client(t)
	server.Stub("GET", "/v1/pairings/pair-1", fakeplatform.JSON(200, map[string]any{"status": "paired"}))
	if _, err := c.PollPairing("pair-1"); err == nil {
		t.Error("paired response without a token accepted")
	}
}

func TestAuthorizedCallsCarryBearerToken(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(200, map[string]any{}))
	server.Stub("POST", "/v1/data-deletions", fakeplatform.JSON(202, map[string]any{}))

	if err := c.RevokeDevice("dev-tok-fake"); err != nil {
		t.Fatal(err)
	}
	if err := c.RequestDeletion("dev-tok-fake", "hash-1"); err != nil {
		t.Fatal(err)
	}

	reqs := server.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	for _, r := range reqs {
		if r.Header.Get("Authorization") != "Bearer dev-tok-fake" {
			t.Errorf("%s %s authorization = %q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
	}
	var body map[string]string
	if err := json.Unmarshal(reqs[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["project_id_hash"] != "hash-1" {
		t.Errorf("deletion body = %v", body)
	}
}

func TestCallsIdentifyThisTrajectorBuild(t *testing.T) {
	server := fakeplatform.New(t)
	c := platform.New(server.URL(), "1.2.3")
	server.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(200, map[string]any{}))

	if err := c.RevokeDevice("dev-tok-fake"); err != nil {
		t.Fatal(err)
	}
	reqs := server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if got := reqs[0].Header.Get("User-Agent"); got != "trajector/1.2.3" {
		t.Errorf("User-Agent = %q, want %q", got, "trajector/1.2.3")
	}
}

func TestNon2xxCarriesItsStatusAndBody(t *testing.T) {
	t.Run("a 503 is temporary", func(t *testing.T) {
		c, server := client(t)
		server.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(503, map[string]any{"error": "down"}))

		err := c.RevokeDevice("dev-tok-fake")
		var status *platform.StatusError
		if !errors.As(err, &status) {
			t.Fatalf("err = %v, want a StatusError", err)
		}
		if status.StatusCode != 503 || !status.Temporary() {
			t.Errorf("status = %d, Temporary = %v, want a temporary 503", status.StatusCode, status.Temporary())
		}
		if !strings.Contains(string(status.Body), "down") {
			t.Errorf("body = %q, want the service's own words kept", status.Body)
		}
	})
	t.Run("a 401 on revoke is the already-revoked goal state", func(t *testing.T) {
		c, server := client(t)
		server.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(401, map[string]any{"error": "unknown token"}))

		err := c.RevokeDevice("dev-tok-fake")
		if !errors.Is(err, platform.ErrAlreadyRevoked) {
			t.Errorf("err = %v, want ErrAlreadyRevoked", err)
		}
	})
	t.Run("a 400 on revoke stays a permanent StatusError", func(t *testing.T) {
		c, server := client(t)
		server.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(400, map[string]any{"error": "malformed"}))

		err := c.RevokeDevice("dev-tok-fake")
		var status *platform.StatusError
		if !errors.As(err, &status) || status.StatusCode != 400 || status.Temporary() {
			t.Errorf("err = %v, want a permanent 400 StatusError", err)
		}
	})
}

func TestPollIntervalFloorsMissingSuggestions(t *testing.T) {
	if got := (platform.Pairing{}).PollInterval(); got != 2*time.Second {
		t.Errorf("PollInterval with no suggestion = %v, want the 2s floor", got)
	}
	if got := (platform.Pairing{PollIntervalMS: 250}).PollInterval(); got != 250*time.Millisecond {
		t.Errorf("PollInterval = %v, want the service's suggestion", got)
	}
}
