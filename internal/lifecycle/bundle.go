package lifecycle

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// A diagnostic archive is named doctorBundlePrefix + timestamp +
// doctorBundleSuffix; the ignore rules are spelled from the same
// constants so they match every name DoctorBundle can produce.
const (
	doctorBundlePrefix = "trajector-doctor-"
	doctorBundleSuffix = ".tar.gz"
)

// doctorBundleIgnoreRules cover both forms a bundle takes inside the
// user's project: the archive itself and the directory left by
// unpacking it.
var doctorBundleIgnoreRules = []string{
	doctorBundlePrefix + "*" + doctorBundleSuffix,
	doctorBundlePrefix + "*/",
}

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
	b.add("info.json", report.InfoJSON(d, m.deps.Now()))
	b.add("diagnosis.json", report.DiagnosisJSON(d))

	uploadDir := m.deps.Layout.UploadDir()
	for _, name := range upload.BookkeepingFiles() {
		// Bookkeeping files hold only ids, timestamps, and service
		// settings by design; they are copied verbatim.
		if data, err := fsatomic.ReadFile(filepath.Join(uploadDir, name)); err == nil {
			b.add("upload/"+name, data)
		}
	}

	routingSummary, err := m.summarizeRouting()
	if err != nil {
		return "", err
	}
	b.add("routing.json", routingSummary)

	// The archive lands in the user's project and names every project
	// root on the machine; neither it nor the directory a user unpacks
	// it into may ride along into their repository.
	for _, rule := range doctorBundleIgnoreRules {
		if _, err := claudesettings.EnsureGitIgnored(d.Project.Root, rule); err != nil {
			return "", err
		}
	}

	name := doctorBundlePrefix + m.deps.Now().UTC().Format("20060102-150405") + doctorBundleSuffix
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

// summarizeRouting reads the routing table for the bundle. What of it
// is safe to write down is the renderer's decision, not this method's.
func (m *Machine) summarizeRouting() ([]byte, error) {
	paused, err := m.routes.PausedReason()
	if err != nil {
		return nil, err
	}
	grants, err := m.routes.All()
	if err != nil {
		return nil, err
	}
	return report.RoutingJSON(paused, grants), nil
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
	if !strings.HasSuffix(path, doctorBundleSuffix) {
		return fmt.Errorf("bundle path %s must end in %s", path, doctorBundleSuffix)
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
