package userdirs_test

import (
	"path/filepath"
	"reflect"
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
