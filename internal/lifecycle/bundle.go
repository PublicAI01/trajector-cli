package lifecycle

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// DoctorBundle writes a diagnostic archive into the current directory
// and returns its path. The bundle is the only way a crash report
// leaves this machine, and the user sends it themselves — so it
// carries diagnostics only: identities, counters, timestamps, and
// reasons. Captured record data, credentials, and clear-text tokens
// never enter it. It is read-only: a bug report preserves the state
// being diagnosed, so nothing is repaired on the way — that is
// doctor's job.
func (m *Machine) DoctorBundle(projectDir string, io IO) (string, error) {
	d, err := m.Diagnose(projectDir)
	if err != nil {
		return "", err
	}

	var b bundle
	b.add("info.json", mustJSON(map[string]any{
		"version":      m.deps.Version,
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"generated_at": m.deps.Now().UTC(),
		"proxy_addr":   d.Proxy.Addr,
	}))
	b.add("diagnosis.json", renderDiagnosis(d))

	uploadDir := m.deps.Layout.UploadDir()
	for _, name := range []string{"state.json", "handshake.json", "pending.json"} {
		// These bookkeeping files hold only ids, timestamps, and service
		// settings by design; they are copied verbatim.
		if data, err := os.ReadFile(filepath.Join(uploadDir, name)); err == nil {
			b.add("upload/"+name, data)
		}
	}

	routingSummary, err := m.summarizeRouting()
	if err != nil {
		return "", err
	}
	b.add("routing.json", routingSummary)

	// The archive lands in the user's project and names every project
	// root on the machine; it must never ride along into their repository.
	if _, err := claudesettings.EnsureGitIgnored(d.Project.Root, "trajector-doctor-*.tar.gz"); err != nil {
		return "", err
	}

	name := fmt.Sprintf("trajector-doctor-%s.tar.gz", m.deps.Now().UTC().Format("20060102-150405"))
	if err := b.write(name); err != nil {
		return "", err
	}
	path, err := filepath.Abs(name)
	if err != nil {
		path = name
	}
	fmt.Fprintf(io.Out, "Wrote %s\n", path)
	fmt.Fprintln(io.Out, "It contains diagnostics only: no captured data, no credentials, no clear-text")
	fmt.Fprintln(io.Out, "tokens. Review its contents, then attach it to your report.")
	return path, nil
}

// maskedToken masks itself when marshalled, so a token field added to
// the diagnosis rendering is masked by construction, not by someone
// remembering to call the masking function.
type maskedToken string

func (t maskedToken) MarshalJSON() ([]byte, error) {
	return json.Marshal(maskToken(string(t)))
}

// errString renders an error for the diagnosis, empty when nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The diagnosis wire shapes. Token fields are typed maskedToken;
// everything else in a Diagnosis is identities, counters, timestamps,
// and reasons.
type diagnosisWire struct {
	Project    projectWire    `json:"project"`
	Proxy      proxyWire      `json:"proxy"`
	Spool      spoolWire      `json:"spool"`
	Uploads    upload.State   `json:"uploads"`
	Rejected   []rejectedWire `json:"rejected"`
	Handshake  any            `json:"handshake"`
	TokenStore tokenStoreWire `json:"token_store"`
}

type projectWire struct {
	Root             string      `json:"root"`
	ProjectIDHash    string      `json:"project_id_hash"`
	Enabled          bool        `json:"enabled"`
	Upstream         string      `json:"upstream"`
	Injected         bool        `json:"injected"`
	InjectedToken    maskedToken `json:"injected_token"`
	Token            maskedToken `json:"token"`
	HooksInstalled   bool        `json:"hooks_installed"`
	AgreementVersion string      `json:"agreement_version"`
	ConsentState     string      `json:"consent_state"`
	PauseReason      string      `json:"pause_reason"`
}

type proxyWire struct {
	Addr   string `json:"addr"`
	Holder string `json:"holder"`
	// Health is present only when the holder is ours.
	Health any `json:"health,omitempty"`
}

type spoolWire struct {
	OpenErr     string             `json:"open_err,omitempty"`
	UsageBytes  int64              `json:"usage_bytes"`
	QuotaBytes  int64              `json:"quota_bytes"`
	WritableErr string             `json:"writable_err,omitempty"`
	Days        []spool.DaySummary `json:"days"`
}

type rejectedWire struct {
	BatchID string           `json:"batch_id"`
	Records int              `json:"records"`
	Reason  upload.Rejection `json:"reason"`
}

type tokenStoreWire struct {
	Paired bool   `json:"paired"`
	Err    string `json:"err,omitempty"`
}

// renderDiagnosis serializes a Diagnosis for the bundle.
func renderDiagnosis(d Diagnosis) []byte {
	rejected := make([]rejectedWire, 0, len(d.Rejected))
	for _, b := range d.Rejected {
		rejected = append(rejected, rejectedWire{BatchID: b.BatchID, Records: b.Records, Reason: b.Reason})
	}
	days := d.Spool.Days
	if days == nil {
		days = []spool.DaySummary{}
	}
	proxy := proxyWire{Addr: d.Proxy.Addr, Holder: d.Proxy.Holder.String()}
	if d.Proxy.Holder == proxylife.HolderOurs {
		proxy.Health = d.Proxy.Health
	}
	return mustJSON(diagnosisWire{
		Project: projectWire{
			Root:             d.Project.Root,
			ProjectIDHash:    d.Project.Hash,
			Enabled:          d.Project.Enabled,
			Upstream:         maskUpstreamCredentials(d.Project.Upstream),
			Injected:         d.Project.Injected(),
			InjectedToken:    maskedToken(d.Project.InjectedToken),
			Token:            maskedToken(d.Project.Token),
			HooksInstalled:   d.Project.HookInstalled,
			AgreementVersion: d.Project.AgreementVersion,
			ConsentState:     string(d.Project.ConsentState),
			PauseReason:      string(d.Project.PauseReason),
		},
		Proxy: proxy,
		Spool: spoolWire{
			OpenErr:     errString(d.Spool.OpenErr),
			UsageBytes:  d.Spool.Usage,
			QuotaBytes:  d.Spool.Quota,
			WritableErr: errString(d.Spool.WritableErr),
			Days:        days,
		},
		Uploads:    d.Uploads,
		Rejected:   rejected,
		Handshake:  d.Handshake,
		TokenStore: tokenStoreWire{Paired: d.TokenStore.Paired, Err: errString(d.TokenStore.Err)},
	})
}

// summarizeRouting reports the routing table with every token masked.
func (m *Machine) summarizeRouting() ([]byte, error) {
	paused, err := m.routes.PausedReason()
	if err != nil {
		return nil, err
	}
	grants, err := m.routes.All()
	if err != nil {
		return nil, err
	}
	type project struct {
		RootPath      string      `json:"root_path"`
		ProjectIDHash string      `json:"project_id_hash"`
		Upstream      string      `json:"upstream"`
		GrantedAt     string      `json:"granted_at"`
		Revoked       bool        `json:"revoked"`
		Token         maskedToken `json:"token"`
	}
	summary := struct {
		PausedReason string    `json:"paused_reason"`
		Projects     []project `json:"projects"`
	}{PausedReason: string(paused), Projects: []project{}}
	for _, g := range grants {
		summary.Projects = append(summary.Projects, project{
			RootPath:      g.RootPath,
			ProjectIDHash: g.ProjectIDHash,
			Upstream:      maskUpstreamCredentials(g.Upstream),
			GrantedAt:     g.GrantedAt,
			Revoked:       g.Revoked,
			Token:         maskedToken(g.Token),
		})
	}
	return mustJSON(summary), nil
}

// maskUpstreamCredentials strips the credentials a user-configured relay
// URL may carry — userinfo and query values — before the upstream is
// written into a bundle the user shares. The host and path stay so the
// diagnosis still shows where traffic was routed. A value that is not a
// URL is returned unchanged; it carried nothing to strip.
func maskUpstreamCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			q.Set(k, "redacted")
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// maskToken keeps just enough of a token to correlate entries across
// the bundle without disclosing it.
func maskToken(token string) string {
	if len(token) <= 8 {
		if token == "" {
			return ""
		}
		return "masked"
	}
	return token[:8] + "…(masked)"
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Every value marshalled here is built from plain maps and
		// structs; failure is a programming error.
		panic(err)
	}
	return append(data, '\n')
}

// bundle accumulates named entries and writes them as one tar.gz.
type bundle struct {
	entries []bundleEntry
}

type bundleEntry struct {
	name string
	data []byte
}

func (b *bundle) add(name string, data []byte) {
	b.entries = append(b.entries, bundleEntry{name, data})
}

func (b *bundle) write(path string) error {
	if !strings.HasSuffix(path, ".tar.gz") {
		return fmt.Errorf("bundle path %s must end in .tar.gz", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range b.entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.data))}); err != nil {
			f.Close()
			return err
		}
		if _, err := tw.Write(e.data); err != nil {
			f.Close()
			return err
		}
	}
	for _, finish := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := finish(); err != nil {
			return err
		}
	}
	return nil
}
