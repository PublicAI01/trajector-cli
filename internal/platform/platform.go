// Package platform is the HTTP client for the trajector service API:
// device pairing, device revocation, data-deletion requests, and batch
// uploads. The capture proxy never uses this client; forwarding has its
// own transport and its own upstreams.
package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultBaseURL is the production service endpoint. Tests and
// self-hosted setups override it through the TRAJECTOR_PLATFORM_URL
// environment variable, read by the CLI.
const DefaultBaseURL = "https://trajector-api.publicai.io"

// BaseURLEnv names the environment override for the service endpoint.
const BaseURLEnv = "TRAJECTOR_PLATFORM_URL"

// Pairing statuses reported by the service.
const (
	PairingPending = "pending"
	PairingPaired  = "paired"
	PairingExpired = "expired"
)

// StatusError reports a non-2xx answer from the service, carrying the
// status and body so a caller can distinguish already-done from
// try-again-later instead of guessing from a flattened string. Every
// operation without a more specific error class returns it.
type StatusError struct {
	StatusCode int
	Status     string
	Method     string
	Path       string
	Body       []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("platform: %s %s: %s", e.Method, e.Path, e.Status)
}

// Temporary reports whether retrying the same call later could
// succeed: service-side failures and rate limits are temporary, client
// errors are not.
func (e *StatusError) Temporary() bool {
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests || e.StatusCode == http.StatusRequestTimeout
}

// Client calls the trajector service.
type Client struct {
	baseURL string
	http    *http.Client
}

// New builds a client for the given endpoint, identifying itself as
// this trajector build.
func New(baseURL, version string) *Client {
	return &Client{baseURL: baseURL, http: newClient(version)}
}

// Pairing is one in-progress device pairing.
type Pairing struct {
	PairingID string `json:"pairing_id"`
	// VerificationURL is opened by the user in a browser to approve
	// this device.
	VerificationURL string `json:"verification_url"`
	// UserCode is shown so the user can match the browser page to this
	// terminal.
	UserCode       string `json:"user_code"`
	PollIntervalMS int    `json:"poll_interval_ms"`
}

// PollInterval returns the server-suggested poll cadence with a floor
// that keeps a missing or zero suggestion from turning into a busy
// loop.
func (p Pairing) PollInterval() time.Duration {
	if p.PollIntervalMS <= 0 {
		return 2 * time.Second
	}
	return time.Duration(p.PollIntervalMS) * time.Millisecond
}

// StartPairing begins a device pairing.
func (c *Client) StartPairing(clientVersion string) (Pairing, error) {
	var p Pairing
	err := c.call(http.MethodPost, "/v1/pairings", "", map[string]string{
		"client_version": clientVersion,
	}, &p)
	if err != nil {
		return Pairing{}, err
	}
	if p.PairingID == "" || p.VerificationURL == "" {
		return Pairing{}, fmt.Errorf("platform: pairing response is missing pairing_id or verification_url")
	}
	return p, nil
}

// PairingResult is the state of one pairing poll.
type PairingResult struct {
	Status      string `json:"status"`
	DeviceToken string `json:"device_token"`
}

// PollPairing checks whether the user approved the pairing.
func (c *Client) PollPairing(pairingID string) (PairingResult, error) {
	var r PairingResult
	if err := c.call(http.MethodGet, "/v1/pairings/"+pairingID, "", nil, &r); err != nil {
		return PairingResult{}, err
	}
	if r.Status == PairingPaired && r.DeviceToken == "" {
		return PairingResult{}, fmt.Errorf("platform: paired response carried no device token")
	}
	return r, nil
}

// RevokeDevice revokes the device token server-side.
func (c *Client) RevokeDevice(deviceToken string) error {
	return c.call(http.MethodPost, "/v1/device/revoke", deviceToken, struct{}{}, nil)
}

// RequestDeletion asks the service to delete this project's uploaded
// but not yet delivered data.
func (c *Client) RequestDeletion(deviceToken, projectIDHash string) error {
	return c.call(http.MethodPost, "/v1/data-deletions", deviceToken, map[string]string{
		"project_id_hash": projectIDHash,
	}, nil)
}

func (c *Client) call(method, path, bearer string, reqBody, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, Method: method, Path: path, Body: data}
	}
	if respBody == nil {
		return nil
	}
	if err := json.Unmarshal(data, respBody); err != nil {
		return fmt.Errorf("platform: decoding %s %s response: %w", method, path, err)
	}
	return nil
}
