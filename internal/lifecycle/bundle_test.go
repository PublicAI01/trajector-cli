package lifecycle_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// readBundle extracts every entry of a diagnostic bundle.
func readBundle(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = string(data)
	}
	return entries
}

func TestDoctorBundleContainsTheDiagnosticSurfaces(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	writeUploadFile(t, e, "state.json", map[string]any{"last_error": "boom"})
	writeUploadFile(t, e, "handshake.json", map[string]any{"min_client_version": "9.9.9"})
	seedRejectedBatch(t, e, "b-poison", "413 Request Entity Too Large", map[string][]byte{
		"req-1": spooledEnvelope(t, "req-1", e.deps.Now()),
	})

	out := t.TempDir()
	e.stdout.Reset()
	path, err := e.machine().DoctorBundle(e.project, out, e.io())
	if err != nil {
		t.Fatalf("bundle: %v\nstdout: %s", err, e.stdout)
	}
	if filepath.Dir(path) != out {
		t.Errorf("bundle written to %s, want it inside %s", path, out)
	}
	if !strings.Contains(e.stdout.String(), filepath.Base(path)) {
		t.Errorf("stdout = %q, want the bundle path reported", e.stdout)
	}

	entries := readBundle(t, path)
	for name, want := range map[string]string{
		"info.json":                     "testv",
		"doctor.txt":                    "trajector testv doctor",
		"healthz.json":                  "trajector-proxy",
		"upload/state.json":             "boom",
		"upload/handshake.json":         "9.9.9",
		"spool.json":                    "usage_bytes",
		"rejected/b-poison/reason.json": "413 Request Entity Too Large",
		"routing.json":                  e.canonicalRoot(),
		"project.json":                  "hooks_installed",
	} {
		got, ok := entries[name]
		if !ok {
			t.Errorf("bundle is missing %s (has: %v)", name, keysOf(entries))
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s = %q, want it to contain %q", name, got, want)
		}
	}
}

func TestDoctorBundleNeverLeaksRecordDataOrTokens(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	token := e.status().Token
	const marker = "MARKER-not-for-export"
	e.sandbox.SeedRawcall("req-spooled-"+marker, "hash-project", e.deps.Now())
	seedRejectedBatch(t, e, "b-poison", "", map[string][]byte{
		"req-1": []byte(`{"request_id":"req-1","request":{"secret":"` + marker + `"}}`),
	})

	path, err := e.machine().DoctorBundle(e.project, t.TempDir(), e.io())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range readBundle(t, path) {
		if strings.Contains(data, marker) {
			t.Errorf("bundle entry %s contains record data", name)
		}
		if strings.Contains(data, token) {
			t.Errorf("bundle entry %s contains the project token in the clear", name)
		}
	}
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestDoctorBundleIsGitIgnoredInTheProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	e := newEnv(t)
	t.Setenv("HOME", e.deps.Home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.deps.Home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(e.deps.Home, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = e.project
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	path, err := e.machine().DoctorBundle(e.project, e.canonicalRoot(), e.io())
	if err != nil {
		t.Fatal(err)
	}
	check := exec.Command("git", "check-ignore", "-q", "--", filepath.Base(path))
	check.Dir = e.canonicalRoot()
	if err := check.Run(); err != nil {
		t.Errorf("the bundle %s is not git-ignored in the project", filepath.Base(path))
	}

	before, err := os.ReadFile(filepath.Join(e.canonicalRoot(), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.machine().DoctorBundle(e.project, e.canonicalRoot(), e.io()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(e.canonicalRoot(), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf(".gitignore grew on a second bundle:\nbefore: %q\nafter: %q", before, after)
	}
}
