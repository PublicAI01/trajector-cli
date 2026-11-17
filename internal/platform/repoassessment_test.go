package platform_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/redact"
)

const assessmentBody = `{"format":1}`

func submit(t *testing.T, resp fakeplatform.Response) (string, *fakeplatform.Server, error) {
	t.Helper()
	c, server := client(t)
	server.AnswersRepoAssessment(resp)
	id, err := c.SubmitRepoAssessment(t.Context(), "dev-tok-fake", redact.AlreadyRedacted([]byte(assessmentBody)))
	return id, server, err
}

func outcome(id string, err error) platform.RepoAssessmentOutcome {
	if err == nil && id != "" {
		return platform.RepoAssessmentStored
	}
	if e, ok := errors.AsType[*platform.RepoAssessmentError](err); ok {
		return e.Outcome
	}
	return "an error of no outcome"
}

func TestSubmitRepoAssessment_SendsTheBodyGzippedWithTheDeviceCredential(t *testing.T) {
	id, server, err := submit(t, fakeplatform.JSON(200, map[string]string{"id": "ra-1"}))
	if err != nil || id != "ra-1" {
		t.Fatalf("SubmitRepoAssessment = %q, %v", id, err)
	}
	got := server.RepoAssessments()
	if len(got) != 1 {
		t.Fatalf("%d submissions received, want 1", len(got))
	}
	body, err := fakeplatform.RepoAssessmentBody(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != assessmentBody {
		t.Errorf("body = %q, want %q", body, assessmentBody)
	}
	for header, want := range map[string]string{
		"Authorization": "Bearer dev-tok-fake",
		"Content-Type":  "application/json",
		"User-Agent":    "trajector/test",
	} {
		if v := got[0].Header.Get(header); v != want {
			t.Errorf("%s = %q, want %q", header, v, want)
		}
	}
}

func TestSubmitRepoAssessment_ReadsEachAnswerAsOneOutcome(t *testing.T) {
	retry := fakeplatform.JSON(429, map[string]string{"error": "rate_limited"})
	retry.Header.Set("Retry-After", "30")
	tests := []struct {
		name string
		resp fakeplatform.Response
		want platform.RepoAssessmentOutcome
	}{
		{"an id", fakeplatform.JSON(201, map[string]string{"id": "ra_2-x"}), platform.RepoAssessmentStored},
		{"no id", fakeplatform.JSON(200, map[string]string{}), platform.RepoAssessmentFailed},
		{"an empty id", fakeplatform.JSON(200, map[string]string{"id": ""}), platform.RepoAssessmentFailed},
		{"an id of another shape", fakeplatform.JSON(200, map[string]string{"id": "ra 1\x1b[2J"}), platform.RepoAssessmentFailed},
		{"an id too long", fakeplatform.JSON(200, map[string]string{"id": strings.Repeat("a", 65)}), platform.RepoAssessmentFailed},
		{"a body that is not JSON", fakeplatform.Response{Status: 200, Body: []byte("ok")}, platform.RepoAssessmentFailed},
		{"401", fakeplatform.JSON(401, map[string]string{"error": "unauthorized"}), platform.RepoAssessmentRelogin},
		{"426", fakeplatform.Refuses426("9.9.9", "upgrade"), platform.RepoAssessmentUpgradeClient},
		{"400 terms version", fakeplatform.JSON(400, map[string]string{"error": "consent_version_unsupported"}), platform.RepoAssessmentUpgradeTerms},
		{"400 invalid", fakeplatform.JSON(400, map[string]string{"error": "invalid_request"}), platform.RepoAssessmentFailed},
		{"404", fakeplatform.JSON(404, map[string]string{"error": "not_found"}), platform.RepoAssessmentUnavailable},
		{"413", fakeplatform.JSON(413, map[string]string{"error": "payload_too_large"}), platform.RepoAssessmentTooLarge},
		{"429", retry, platform.RepoAssessmentRetryLater},
		{"403", fakeplatform.JSON(403, map[string]string{"error": "forbidden"}), platform.RepoAssessmentFailed},
		{"500", fakeplatform.JSON(500, map[string]string{"error": "internal_error"}), platform.RepoAssessmentFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, _, err := submit(t, tt.resp)
			if got := outcome(id, err); got != tt.want {
				t.Errorf("outcome = %q (%v), want %q", got, err, tt.want)
			}
			if tt.want != platform.RepoAssessmentStored && id != "" {
				t.Errorf("a failure returned the id %q", id)
			}
		})
	}
}

func TestSubmitRepoAssessment_KeepsOnlyAnErrorCodeOfTheContractShape(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"internal_error", "internal_error"},
		{"Internal Error", ""},
		{"\x1b[31mred", ""},
		{strings.Repeat("a", 65), ""},
	}
	for _, tt := range tests {
		_, _, err := submit(t, fakeplatform.JSON(500, map[string]string{"error": tt.code}))
		e, ok := errors.AsType[*platform.RepoAssessmentError](err)
		if !ok || e.Code != tt.want {
			t.Errorf("error %q: code = %+v, want %q", tt.code, e, tt.want)
		}
	}
}

func TestSubmitRepoAssessment_StatesTheVersionAndPauseTheServiceNamed(t *testing.T) {
	_, _, err := submit(t, fakeplatform.Refuses426("9.9.9", "please\x1b[2J upgrade"))
	e, ok := errors.AsType[*platform.RepoAssessmentError](err)
	if !ok || e.MinClientVersion != "9.9.9" || e.Message != "please [2J upgrade" {
		t.Errorf("426 = %+v", e)
	}
	retry := fakeplatform.JSON(429, map[string]string{})
	retry.Header.Set("Retry-After", "30")
	_, _, err = submit(t, retry)
	if e, ok := errors.AsType[*platform.RepoAssessmentError](err); !ok || e.RetryAfter != 30*time.Second {
		t.Errorf("429 = %+v", e)
	}
}

func TestSubmitRepoAssessment_SendsNothingToARefusedEndpoint(t *testing.T) {
	c := platform.New("http://example.com", "test")
	_, err := c.SubmitRepoAssessment(t.Context(), "dev-tok-fake", redact.AlreadyRedacted([]byte(assessmentBody)))
	if outcome("", err) != platform.RepoAssessmentFailed {
		t.Fatalf("err = %v, want a failure before anything is sent", err)
	}
}

func TestSharedFixtures_EachRepoAssessmentAnswerHasTheOutcomeTheContractNames(t *testing.T) {
	dir, tried := conformance.Find()
	if dir == "" {
		if os.Getenv(conformance.StrictEnv) == "1" {
			t.Fatalf("shared contract fixtures not found; looked in:\n  %s", strings.Join(tried, "\n  "))
		}
		t.Skipf("shared contract fixtures not found — this layer is not running.\nLooked in:\n  %s\nSet %s to point at them, or %s=1 to make this a failure.",
			strings.Join(tried, "\n  "), conformance.DirEnv, conformance.StrictEnv)
	}
	cases, err := conformance.LoadRepoAssessments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatalf("no repository assessment fixtures under %s — a layer that checks nothing must not report success", dir)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			body, err := json.Marshal(c.Response.Body)
			if err != nil {
				t.Fatal(err)
			}
			header := http.Header{}
			for k, v := range c.Response.Headers {
				header.Set(k, v)
			}
			id, _, err := submit(t, fakeplatform.Response{Status: c.Response.Status, Header: header, Body: body})
			if got := outcome(id, err); got != c.Meta.Expect {
				t.Fatalf("outcome = %q (%v), want %q", got, err, c.Meta.Expect)
			}
			if c.Meta.Expect == platform.RepoAssessmentStored && id != c.Response.Body["id"] {
				t.Errorf("id = %q, want %v", id, c.Response.Body["id"])
			}
		})
	}
}
