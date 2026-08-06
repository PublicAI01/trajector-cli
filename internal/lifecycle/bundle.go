package lifecycle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// DoctorBundle writes a diagnostic archive into outDir and returns its
// path. The bundle is the only way a crash report leaves this machine,
// and the user sends it themselves — so it carries diagnostics only:
// identities, counters, timestamps, and reasons. Captured record data,
// credentials, and clear-text tokens never enter it.
func (m *Machine) DoctorBundle(projectDir, outDir string, io IO) (string, error) {
	var b bundle

	b.add("info.json", mustJSON(map[string]any{
		"version":      m.deps.Version,
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"generated_at": m.deps.Now().UTC(),
		"proxy_addr":   m.proxy.Addr(),
	}))

	// Running doctor may also repair what is safely repairable — every
	// CLI touchpoint self-heals.
	var report bytes.Buffer
	if _, err := m.Doctor(projectDir, IO{In: io.In, Out: &report, Err: io.Err}); err != nil {
		return "", err
	}
	b.add("doctor.txt", report.Bytes())

	if h, running := m.proxy.Health(); running && h.Service == apiproxy.ServiceName {
		b.add("healthz.json", mustJSON(h))
	}

	uploadDir := m.deps.Layout.UploadDir()
	for _, name := range []string{"state.json", "handshake.json", "pending.json"} {
		// These bookkeeping files hold only ids, timestamps, and service
		// settings by design; they are copied verbatim.
		if data, err := os.ReadFile(filepath.Join(uploadDir, name)); err == nil {
			b.add("upload/"+name, data)
		}
	}

	spoolSummary, err := summarizeSpool(m.deps.Layout.SpoolDir())
	if err != nil {
		return "", err
	}
	b.add("spool.json", spoolSummary)

	rejected, err := upload.ListRejected(m.deps.Layout.RejectedDir())
	if err != nil {
		return "", err
	}
	for _, batch := range rejected {
		// Only the reason ever leaves; the quarantined records themselves
		// are captured data.
		path := filepath.Join(m.deps.Layout.RejectedDir(), batch.BatchID, "reason.json")
		if data, err := os.ReadFile(path); err == nil {
			b.add("rejected/"+batch.BatchID+"/reason.json", data)
		}
	}

	routingSummary, err := m.summarizeRouting()
	if err != nil {
		return "", err
	}
	b.add("routing.json", routingSummary)

	st, err := m.Project(projectDir)
	if err != nil {
		return "", err
	}
	b.add("project.json", mustJSON(map[string]any{
		"root":              st.Root,
		"project_id_hash":   st.Hash,
		"enabled":           st.Enabled,
		"upstream":          st.Upstream,
		"injected":          st.Injected(),
		"injected_token":    maskToken(st.InjectedToken),
		"token":             maskToken(st.Token),
		"hooks_installed":   st.HookInstalled,
		"agreement_version": st.AgreementVersion,
		"consent_state":     st.ConsentState,
		"pause_reason":      st.PauseReason,
	}))

	// The archive lands in the user's project and names every project
	// root on the machine; it must never ride along into their repository.
	if _, err := claudesettings.EnsureGitIgnored(st.Root, "trajector-doctor-*.tar.gz"); err != nil {
		return "", err
	}

	name := fmt.Sprintf("trajector-doctor-%s.tar.gz", m.deps.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(outDir, name)
	if err := b.write(path); err != nil {
		return "", err
	}
	fmt.Fprintf(io.Out, "Wrote %s\n", path)
	fmt.Fprintln(io.Out, "It contains diagnostics only: no captured data, no credentials, no clear-text")
	fmt.Fprintln(io.Out, "tokens. Review its contents, then attach it to your report.")
	return path, nil
}

// summarizeSpool reports per-day counts and sizes, never file names:
// request ids belong to the records, not the diagnostics.
func summarizeSpool(dir string) ([]byte, error) {
	type day struct {
		Day     string `json:"day"`
		Records int    `json:"records"`
		Bytes   int64  `json:"bytes"`
	}
	summary := struct {
		UsageBytes int64 `json:"usage_bytes"`
		Days       []day `json:"days"`
	}{Days: []day{}}

	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := day{Day: e.Name()}
		files, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if info, err := f.Info(); err == nil {
				d.Bytes += info.Size()
			}
			if filepath.Ext(f.Name()) == ".json" {
				d.Records++
			}
		}
		summary.UsageBytes += d.Bytes
		summary.Days = append(summary.Days, d)
	}
	return mustJSON(summary), nil
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
		RootPath      string `json:"root_path"`
		ProjectIDHash string `json:"project_id_hash"`
		Upstream      string `json:"upstream"`
		GrantedAt     string `json:"granted_at"`
		Revoked       bool   `json:"revoked"`
		Token         string `json:"token"`
	}
	summary := struct {
		PausedReason string    `json:"paused_reason"`
		Projects     []project `json:"projects"`
	}{PausedReason: string(paused), Projects: []project{}}
	for _, g := range grants {
		summary.Projects = append(summary.Projects, project{
			RootPath:      g.RootPath,
			ProjectIDHash: g.ProjectIDHash,
			Upstream:      g.Upstream,
			GrantedAt:     g.GrantedAt,
			Revoked:       g.Revoked,
			Token:         maskToken(g.Token),
		})
	}
	return mustJSON(summary), nil
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
