package lifecycle_test

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
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
	e.sandbox.SeedUpgradeRefusal("9.9.9", "Upload format 0.1.x is retired on 2026-09-01.")
	writeUploadFile(t, e, "pending-unreadable.json", map[string]any{"batch_id": "b-half"})
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison", Details: "413 Request Entity Too Large"},
		map[string][]byte{
			"req-1": spooledEnvelope(t, "req-1", e.deps.Now()),
		})

	out := t.TempDir()
	t.Chdir(out)
	e.stdout.Reset()
	path, err := e.machine().DoctorBundle(e.project, e.io())
	if err != nil {
		t.Fatalf("bundle: %v\nstdout: %s", err, e.stdout)
	}
	if filepath.Dir(path) != out {
		t.Errorf("bundle written to %s, want it inside the current directory %s", path, out)
	}
	if !strings.Contains(e.stdout.String(), filepath.Base(path)) {
		t.Errorf("stdout = %q, want the bundle path reported", e.stdout)
	}

	// routing.json carries the root as a JSON string, where a Windows
	// path's separators arrive escaped; match the encoded spelling
	// rather than the one the filesystem uses.
	rootInJSON, err := json.Marshal(e.canonicalRoot())
	if err != nil {
		t.Fatal(err)
	}

	entries := readBundle(t, path)
	for name, want := range map[string]string{
		"info.json":                      "testv",
		"upload/state.json":              "boom",
		"upload/handshake.json":          "9.9.9",
		"upload/pending-unreadable.json": "b-half",
		"routing.json":                   string(rootInJSON),
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

	diagnosis, ok := entries["diagnosis.json"]
	if !ok {
		t.Fatalf("bundle is missing diagnosis.json (has: %v)", keysOf(entries))
	}
	// One serialized Diagnosis carries what used to be scattered over
	// prose and per-surface files: project, live proxy report, spool
	// days, and the rejected batch's recorded reason.
	for _, want := range []string{
		`"hooks_installed": true`,
		`"holder": "ours"`,
		`"service": "trajector-proxy"`,
		`"usage_bytes"`,
		`"413 Request Entity Too Large"`,
		`"min_client_version": "9.9.9"`,
		// Support reads why uploads were held back as the client itself
		// judged it, with the service's own words beside the judgement:
		// "this client is behind" and "the service wants something else"
		// are different reports and must not have to be told apart by
		// re-deriving anything from the handshake.
		`"reason": "version_gate"`,
		`"message": "Upload format 0.1.x is retired on 2026-09-01."`,
	} {
		if !strings.Contains(diagnosis, want) {
			t.Errorf("diagnosis.json = %s\nwant it to contain %q", diagnosis, want)
		}
	}
}

func TestDoctorBundleRecordsStoreFailuresInsteadOfFailing(t *testing.T) {
	e := newEnv(t)
	e.obstruct(e.layout().SpoolDir())
	e.obstruct(e.layout().RejectedDir())

	t.Chdir(t.TempDir())
	path, err := e.machine().DoctorBundle(e.project, e.io())
	if err != nil {
		t.Fatalf("bundle: %v\nstdout: %s", err, e.stdout)
	}
	diagnosis, ok := readBundle(t, path)["diagnosis.json"]
	if !ok {
		t.Fatal("bundle is missing diagnosis.json")
	}
	for _, want := range []string{`"open_err"`, `"rejected_err"`} {
		if !strings.Contains(diagnosis, want) {
			t.Errorf("diagnosis.json = %s\nwant it to contain %q", diagnosis, want)
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
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison"},
		map[string][]byte{
			"req-1": []byte(`{"request_id":"req-1","request":{"secret":"` + marker + `"}}`),
		})

	t.Chdir(t.TempDir())
	path, err := e.machine().DoctorBundle(e.project, e.io())
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

func TestDoctorBundleStripsUpstreamCredentials(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	// A user routes through their own relay and put credentials in the
	// base URL. The bundle records where traffic went, never the secret.
	e.environ["ANTHROPIC_BASE_URL"] = "https://user:sekret-pw@relay.example.com/v1?api_key=SECRET-KEY"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	path, err := e.machine().DoctorBundle(e.project, e.io())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range readBundle(t, path) {
		for _, secret := range []string{"sekret-pw", "SECRET-KEY"} {
			if strings.Contains(data, secret) {
				t.Errorf("bundle entry %s leaked an upstream credential (%s)", name, secret)
			}
		}
	}
	// The host must survive so the diagnosis still shows the route.
	if diag := readBundle(t, path)["diagnosis.json"]; !strings.Contains(diag, "relay.example.com") {
		t.Errorf("diagnosis.json dropped the upstream host: %s", diag)
	}
}

func TestDoctorBundleRepairsNothing(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	// Drift the injected token — exactly what doctor would repair by
	// rewriting the injection. The bundle records the state, verbatim.
	settings := e.settingsPath()
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(string(data), e.status().Token, "00000000000000000000000000000000", 1)
	if drifted == string(data) {
		t.Fatal("test setup: token not found in the injected settings")
	}
	if err := os.WriteFile(settings, []byte(drifted), 0o600); err != nil {
		t.Fatal(err)
	}
	routingBefore, err := os.ReadFile(e.layout().RoutingTable())
	if err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	if _, err := e.machine().DoctorBundle(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	settingsAfter, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(settingsAfter) != drifted {
		t.Errorf("the bundle rewrote the project settings:\nbefore: %s\nafter: %s", drifted, settingsAfter)
	}
	routingAfter, err := os.ReadFile(e.layout().RoutingTable())
	if err != nil {
		t.Fatal(err)
	}
	if string(routingAfter) != string(routingBefore) {
		t.Errorf("the bundle rewrote the routing table:\nbefore: %s\nafter: %s", routingBefore, routingAfter)
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
	e := newEnv(t)
	e.gitRepo()
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	t.Chdir(e.canonicalRoot())
	path, err := e.machine().DoctorBundle(e.project, e.io())
	if err != nil {
		t.Fatal(err)
	}
	if !e.gitIgnored(filepath.Base(path)) {
		t.Errorf("the bundle %s is not git-ignored in the project", filepath.Base(path))
	}

	before, err := os.ReadFile(filepath.Join(e.canonicalRoot(), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.machine().DoctorBundle(e.project, e.io()); err != nil {
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

func TestDoctorBundleRestoresIgnoreRulesWhenMissing(t *testing.T) {
	e := newEnv(t)
	e.gitRepo()
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.canonicalRoot(), ".gitignore")); err != nil {
		t.Fatal(err)
	}

	t.Chdir(e.canonicalRoot())
	if _, err := e.machine().DoctorBundle(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	ignore, err := os.ReadFile(filepath.Join(e.canonicalRoot(), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"trajector-doctor-*.tar.gz\n", "trajector-doctor-*/\n"} {
		if !strings.Contains(string(ignore), rule) {
			t.Errorf(".gitignore = %q, want it to contain %q", ignore, rule)
		}
	}
}
