package userdirs_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

func env(goos string, m map[string]string) userdirs.Env {
	return userdirs.Env{GOOS: goos, Getenv: func(k string) string { return m[k] }}
}

// Resolve joins with the host's separator while the layouts it is asked
// for belong to whatever GOOS the case names, so a table written for
// linux reads back with backslashes on a Windows runner. slash puts both
// sides in one form: what these tests assert is which directory a file
// lands in, never how the host spells a path.
func slash(p string) string { return filepath.ToSlash(p) }

func slashAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.ToSlash(p)
	}
	return out
}

func TestResolvePlacesFilesPerPlatform(t *testing.T) {
	tests := []struct {
		name                           string
		env                            userdirs.Env
		wantRoutingTable, wantSpoolDir string
		wantProxyLog                   string
		wantErr                        bool
	}{
		{
			name:             "linux defaults",
			env:              env("linux", map[string]string{"HOME": "/home/u"}),
			wantRoutingTable: "/home/u/.config/trajector/proxy_projects.json",
			wantSpoolDir:     "/home/u/.local/share/trajector/rawcalls",
			wantProxyLog:     "/home/u/.local/state/trajector/proxy.log",
		},
		{
			name: "linux partial XDG override",
			env: env("linux", map[string]string{
				"HOME":            "/home/u",
				"XDG_CONFIG_HOME": "/etc/custom",
			}),
			wantRoutingTable: "/etc/custom/trajector/proxy_projects.json",
			wantSpoolDir:     "/home/u/.local/share/trajector/rawcalls",
			wantProxyLog:     "/home/u/.local/state/trajector/proxy.log",
		},
		{
			name:    "linux missing HOME",
			env:     env("linux", map[string]string{}),
			wantErr: true,
		},
		{
			name:             "darwin defaults",
			env:              env("darwin", map[string]string{"HOME": "/Users/u"}),
			wantRoutingTable: "/Users/u/Library/Application Support/trajector/proxy_projects.json",
			wantSpoolDir:     "/Users/u/Library/Application Support/trajector/rawcalls",
			wantProxyLog:     "/Users/u/Library/Application Support/trajector/proxy.log",
		},
		{
			name:    "darwin missing HOME",
			env:     env("darwin", map[string]string{}),
			wantErr: true,
		},
		{
			name: "darwin full XDG override wins",
			env: env("darwin", map[string]string{
				"HOME":            "/Users/u",
				"XDG_CONFIG_HOME": "/tmp/t/config",
				"XDG_DATA_HOME":   "/tmp/t/data",
				"XDG_STATE_HOME":  "/tmp/t/state",
			}),
			wantRoutingTable: "/tmp/t/config/trajector/proxy_projects.json",
			wantSpoolDir:     "/tmp/t/data/trajector/rawcalls",
			wantProxyLog:     "/tmp/t/state/trajector/proxy.log",
		},
		{
			name: "windows defaults",
			env: env("windows", map[string]string{
				"APPDATA":      `C:\Users\u\AppData\Roaming`,
				"LOCALAPPDATA": `C:\Users\u\AppData\Local`,
			}),
			wantRoutingTable: filepath.Join(`C:\Users\u\AppData\Roaming`, "trajector", "proxy_projects.json"),
			wantSpoolDir:     filepath.Join(`C:\Users\u\AppData\Local`, "trajector", "rawcalls"),
			wantProxyLog:     filepath.Join(`C:\Users\u\AppData\Local`, "trajector", "proxy.log"),
		},
		{
			name:             "windows derives from USERPROFILE",
			env:              env("windows", map[string]string{"USERPROFILE": `C:\Users\u`}),
			wantRoutingTable: filepath.Join(`C:\Users\u`, "AppData", "Roaming", "trajector", "proxy_projects.json"),
			wantSpoolDir:     filepath.Join(`C:\Users\u`, "AppData", "Local", "trajector", "rawcalls"),
			wantProxyLog:     filepath.Join(`C:\Users\u`, "AppData", "Local", "trajector", "proxy.log"),
		},
		{
			name:    "windows missing everything",
			env:     env("windows", map[string]string{}),
			wantErr: true,
		},
		{
			name:             "unknown goos falls back to unix conventions",
			env:              env("freebsd", map[string]string{"HOME": "/home/u"}),
			wantRoutingTable: "/home/u/.config/trajector/proxy_projects.json",
			wantSpoolDir:     "/home/u/.local/share/trajector/rawcalls",
			wantProxyLog:     "/home/u/.local/state/trajector/proxy.log",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := userdirs.Resolve(tt.env)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Resolve() = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error: %v", err)
			}
			if slash(got.RoutingTable()) != slash(tt.wantRoutingTable) {
				t.Errorf("RoutingTable() = %q, want %q", got.RoutingTable(), tt.wantRoutingTable)
			}
			if slash(got.SpoolDir()) != slash(tt.wantSpoolDir) {
				t.Errorf("SpoolDir() = %q, want %q", got.SpoolDir(), tt.wantSpoolDir)
			}
			if slash(got.ProxyLog()) != slash(tt.wantProxyLog) {
				t.Errorf("ProxyLog() = %q, want %q", got.ProxyLog(), tt.wantProxyLog)
			}
		})
	}
}

func TestFilesShareTheDirectoryTheyBelongIn(t *testing.T) {
	l, err := userdirs.Resolve(env("linux", map[string]string{"HOME": "/home/u"}))
	if err != nil {
		t.Fatal(err)
	}
	config := "/home/u/.config/trajector"
	for name, got := range map[string]string{
		"ConfigFile":   l.ConfigFile(),
		"ConsentFile":  l.ConsentFile(),
		"SecretsDir":   l.SecretsDir(),
		"RoutingTable": l.RoutingTable(),
	} {
		if slash(filepath.Dir(got)) != config {
			t.Errorf("%s() = %q, want it under %q", name, got, config)
		}
	}
}

func TestRootsCollapseWhenThePlatformSharesOneDirectory(t *testing.T) {
	darwin, err := userdirs.Resolve(env("darwin", map[string]string{"HOME": "/Users/u"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/Users/u/Library/Application Support/trajector"}
	if got := slashAll(darwin.Roots()); !reflect.DeepEqual(got, want) {
		t.Errorf("darwin Roots() = %v, want %v", got, want)
	}

	linux, err := userdirs.Resolve(env("linux", map[string]string{"HOME": "/home/u"}))
	if err != nil {
		t.Fatal(err)
	}
	wantLinux := []string{
		"/home/u/.local/share/trajector",
		"/home/u/.config/trajector",
		"/home/u/.local/state/trajector",
	}
	if got := slashAll(linux.Roots()); !reflect.DeepEqual(got, wantLinux) {
		t.Errorf("linux Roots() = %v, want %v", got, wantLinux)
	}
}

// tempLayout resolves a layout whose directories live under a fresh
// temp dir, for the candidate listings that read the state directory.
func tempLayout(t *testing.T) (userdirs.Layout, string) {
	t.Helper()
	dir := t.TempDir()
	l, err := userdirs.Resolve(env("linux", map[string]string{
		"XDG_CONFIG_HOME": dir,
		"XDG_DATA_HOME":   dir,
		"XDG_STATE_HOME":  dir,
	}))
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "trajector")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	return l, state
}

func TestAdminTokenFileNamesAreWindowsSafeAndPerInstance(t *testing.T) {
	l, state := tempLayout(t)
	got := l.AdminTokenFile("127.0.0.1:41100", "aabbccdd")
	want := filepath.Join(state, "admin_token-127.0.0.1_41100-aabbccdd")
	if got != want {
		t.Errorf("AdminTokenFile() = %q, want %q", got, want)
	}
	if strings.ContainsAny(filepath.Base(got), `:<>"|?*`) {
		t.Errorf("AdminTokenFile() base %q carries a character Windows refuses", filepath.Base(got))
	}
	if other := l.AdminTokenFile("127.0.0.1:41100", "eeff0011"); other == got {
		t.Error("two instances at one address share a publication file")
	}
	if other := l.AdminTokenFile("127.0.0.1:53200", "aabbccdd"); other == got {
		t.Error("two addresses share a publication file")
	}
	if legacy := l.LegacyAdminTokenFile(); legacy != filepath.Join(state, "admin_token") {
		t.Errorf("LegacyAdminTokenFile() = %q, want the fixed name", legacy)
	}
}

func TestAdminTokenCandidatesListTheirAddressThenTheFixedName(t *testing.T) {
	l, state := tempLayout(t)
	const addr, otherAddr = "127.0.0.1:41100", "127.0.0.1:53200"
	for _, name := range []string{
		"admin_token-127.0.0.1_41100-aabbccdd",
		"admin_token-127.0.0.1_41100-aabbccdd.tmp",
		"admin_token-127.0.0.1_53200-eeff0011",
		"admin_token",
		"proxy.log",
	} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		filepath.Join(state, "admin_token-127.0.0.1_41100-aabbccdd"),
		filepath.Join(state, "admin_token"),
	}
	if got := l.AdminTokenCandidates(addr); !reflect.DeepEqual(got, want) {
		t.Errorf("AdminTokenCandidates(%q) = %v, want %v", addr, got, want)
	}

	if err := os.Remove(filepath.Join(state, "admin_token")); err != nil {
		t.Fatal(err)
	}
	if got := l.AdminTokenCandidates(otherAddr); !reflect.DeepEqual(got, []string{
		filepath.Join(state, "admin_token-127.0.0.1_53200-eeff0011"),
		filepath.Join(state, "admin_token"),
	}) {
		t.Errorf("AdminTokenCandidates(%q) = %v, want the fixed name listed even when absent", otherAddr, got)
	}
}

func TestStaleAdminTokenFilesSpareOwnAndTheFixedName(t *testing.T) {
	l, state := tempLayout(t)
	const addr = "127.0.0.1:41100"
	for _, name := range []string{
		"admin_token-127.0.0.1_41100-aabbccdd",
		"admin_token-127.0.0.1_41100-eeff0011",
		"admin_token-127.0.0.1_53200-99887766",
		"admin_token",
	} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{filepath.Join(state, "admin_token-127.0.0.1_41100-eeff0011")}
	if got := l.StaleAdminTokenFiles(addr, "aabbccdd"); !reflect.DeepEqual(got, want) {
		t.Errorf("StaleAdminTokenFiles() = %v, want %v", got, want)
	}
}

func TestHostReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/x/config")
	t.Setenv("XDG_DATA_HOME", "/tmp/x/data")
	t.Setenv("XDG_STATE_HOME", "/tmp/x/state")
	got, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		t.Fatalf("Resolve(Host()) error: %v", err)
	}
	want := filepath.Join("/tmp/x/config", "trajector", "proxy_projects.json")
	if got.RoutingTable() != want {
		t.Errorf("RoutingTable() = %q, want %q", got.RoutingTable(), want)
	}
}
