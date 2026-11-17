package platform

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// repoAssessmentsPath is the endpoint a repository assessment is sent
// to.
const repoAssessmentsPath = "/v1/repo-assessments"

// repoAssessmentTimeout bounds one submission end to end. A submission
// is sent once, when the user asks, and never retried.
const repoAssessmentTimeout = 60 * time.Second

// RepoAssessmentOutcome names what this client does about the service's
// answer to a repository assessment. What each status means is decided
// here, once; the command only words it.
type RepoAssessmentOutcome string

const (
	// RepoAssessmentStored: the service answered with an id; the
	// command prints it.
	RepoAssessmentStored RepoAssessmentOutcome = "print_id"
	// RepoAssessmentFailed: any other failure, reported as it is.
	RepoAssessmentFailed RepoAssessmentOutcome = "report_failure"
	// RepoAssessmentRelogin: the service refused this device's
	// credential.
	RepoAssessmentRelogin RepoAssessmentOutcome = "relogin"
	// RepoAssessmentUpgradeClient: the service requires a newer client.
	RepoAssessmentUpgradeClient RepoAssessmentOutcome = "upgrade_client"
	// RepoAssessmentUpgradeTerms: the service does not take the terms
	// version this build shows.
	RepoAssessmentUpgradeTerms RepoAssessmentOutcome = "upgrade_consent"
	// RepoAssessmentUnavailable: the service has no such endpoint.
	RepoAssessmentUnavailable RepoAssessmentOutcome = "endpoint_unavailable"
	// RepoAssessmentTooLarge: the submission is larger than the service
	// takes.
	RepoAssessmentTooLarge RepoAssessmentOutcome = "too_large"
	// RepoAssessmentRetryLater: the service asked to slow down.
	RepoAssessmentRetryLater RepoAssessmentOutcome = "retry_later"
)

// RepoAssessmentError is a submission that was not taken. Nothing about
// it is kept on this machine and nothing retries it.
type RepoAssessmentError struct {
	Outcome RepoAssessmentOutcome
	// Code is the service's error code when it has the shape of one,
	// and empty otherwise: free text from the service is never printed
	// as this client's words.
	Code string
	// MinClientVersion and Message are what a version refusal said,
	// made printable; empty when it said nothing.
	MinClientVersion string
	Message          string
	// RetryAfter is the pause the service asked for, zero when it named
	// none.
	RetryAfter time.Duration
	cause      error
}

func (e *RepoAssessmentError) Error() string {
	msg := "the repository assessment did not go through (" + string(e.Outcome) + ")"
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

func (e *RepoAssessmentError) Unwrap() error { return e.cause }

var (
	// submissionIDShape is the id a stored submission is answered with.
	// Any other value reads as no id: the id is printed, and only a
	// value of this shape is safe to print as it is.
	submissionIDShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// errorCodeShape is the shape of an error code the service names.
	errorCodeShape = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

// SubmitRepoAssessment sends one repository assessment, gzip-compressed,
// and returns the id the service stored it under. The body accepts only
// data certified by the redaction pass. Every failure is a
// *RepoAssessmentError; the caller reports it and keeps nothing.
func (c *Client) SubmitRepoAssessment(ctx context.Context, deviceToken string, body redact.RedactedBytes) (string, error) {
	if c.initErr != nil {
		return "", &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: c.initErr}
	}
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	if _, err := zw.Write(body.Bytes()); err != nil {
		return "", &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: err}
	}
	if err := zw.Close(); err != nil {
		return "", &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+repoAssessmentsPath, &zipped)
	if err != nil {
		return "", &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+deviceToken)

	client := *c.http
	client.Timeout = repoAssessmentTimeout
	resp, err := client.Do(req)
	if err != nil {
		return "", &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", classifyRepoAssessmentFailure(resp, data)
	}
	var reply struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &reply) != nil || !submissionIDShape.MatchString(reply.ID) {
		return "", &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: fmt.Errorf("the service answered with status %d but no submission id", resp.StatusCode)}
	}
	return reply.ID, nil
}

// classifyRepoAssessmentFailure maps a non-2xx answer onto the outcome
// the client acts on.
func classifyRepoAssessmentFailure(resp *http.Response, body []byte) *RepoAssessmentError {
	status := newStatusError(resp, http.MethodPost, repoAssessmentsPath, body)
	var reply struct {
		Error            string `json:"error"`
		MinClientVersion string `json:"min_client_version"`
		Message          string `json:"message"`
	}
	_ = json.Unmarshal(body, &reply)
	e := &RepoAssessmentError{Outcome: RepoAssessmentFailed, cause: status}
	if errorCodeShape.MatchString(reply.Error) {
		e.Code = reply.Error
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		e.Outcome = RepoAssessmentRelogin
	case http.StatusUpgradeRequired:
		e.Outcome = RepoAssessmentUpgradeClient
		e.MinClientVersion = SafeServiceText(reply.MinClientVersion)
		e.Message = SafeServiceText(reply.Message)
	case http.StatusBadRequest:
		if e.Code == "consent_version_unsupported" {
			e.Outcome = RepoAssessmentUpgradeTerms
		}
	case http.StatusNotFound:
		e.Outcome = RepoAssessmentUnavailable
	case http.StatusRequestEntityTooLarge:
		e.Outcome = RepoAssessmentTooLarge
	case http.StatusTooManyRequests:
		e.Outcome = RepoAssessmentRetryLater
		e.RetryAfter = min(retryAfter(resp.Header.Get("Retry-After")), maxRetryAfter)
	}
	return e
}
