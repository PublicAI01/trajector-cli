package claudesettings

import (
	"path/filepath"
	"testing"
)

func TestHostFor_ManagedDirectoryIsFixedPerPlatform(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{name: "linux", goos: "linux", want: "/etc/claude-code"},
		{name: "darwin", goos: "darwin", want: "/Library/Application Support/ClaudeCode"},
		{name: "windows", goos: "windows", want: `C:\Program Files\ClaudeCode`},
		{name: "unknown platform uses the unix location", goos: "freebsd", want: "/etc/claude-code"},
		{
			name: "CLAUDE_CODE_MANAGED_SETTINGS_PATH replaces the directory on every platform",
			goos: "darwin",
			env:  map[string]string{"CLAUDE_CODE_MANAGED_SETTINGS_PATH": "/opt/policy"},
			want: "/opt/policy",
		},
		{
			name: "the configuration directory does not move it",
			goos: "linux",
			env:  map[string]string{"CLAUDE_CONFIG_DIR": "/tmp/t/claude"},
			want: "/etc/claude-code",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HostFor(tt.goos, "/home/u", func(key string) string { return tt.env[key] })
			if got.ManagedDir != tt.want {
				t.Errorf("ManagedDir = %q, want %q", got.ManagedDir, tt.want)
			}
		})
	}
}

func TestHostFor_ConfigurationDirectoryFollowsTheEnvironment(t *testing.T) {
	none := func(string) string { return "" }
	host := HostFor("linux", "/home/u", none)
	if want := filepath.Join("/home/u", ".claude"); host.ConfigDir != want {
		t.Errorf("ConfigDir = %q, want %q", host.ConfigDir, want)
	}
	if want := filepath.Join("/home/u", ".claude", "settings.json"); host.UserSettingsPath() != want {
		t.Errorf("UserSettingsPath() = %q, want %q", host.UserSettingsPath(), want)
	}

	moved := HostFor("linux", "/home/u", func(key string) string {
		return map[string]string{"CLAUDE_CONFIG_DIR": "/elsewhere/claude"}[key]
	})
	if moved.ConfigDir != "/elsewhere/claude" {
		t.Errorf("ConfigDir = %q, want the directory the environment names", moved.ConfigDir)
	}
	if want := filepath.Join("/elsewhere/claude", "settings.json"); moved.UserSettingsPath() != want {
		t.Errorf("UserSettingsPath() = %q, want %q", moved.UserSettingsPath(), want)
	}
}

func TestJudgeHookPolicy_ReadsTheConfigurationDirectoryTheEnvironmentNames(t *testing.T) {
	root := t.TempDir()
	getenv := func(key string) string {
		return map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(root, "elsewhere")}[key]
	}
	host := HostFor("linux", filepath.Join(root, "home"), getenv)
	writeFileAt(t, filepath.Join(root, "home", ".claude", "settings.json"), `{"disableAllHooks": true}`)
	if got := JudgeHookPolicy(filepath.Join(root, "project"), host, getenv); !got.Runs {
		t.Errorf("JudgeHookPolicy() = %+v, want the unused default directory left unread", got)
	}
	writeFileAt(t, host.UserSettingsPath(), `{"disableAllHooks": true}`)
	if got := JudgeHookPolicy(filepath.Join(root, "project"), host, getenv); got.Runs {
		t.Errorf("JudgeHookPolicy() = %+v, want the named directory read", got)
	}
}

func TestIsolate_PointsEveryClaudeDirectoryInsideTheTree(t *testing.T) {
	root := t.TempDir()
	set := map[string]string{}
	Isolate(func(key, value string) { set[key] = value }, root)

	host := HostFor("darwin", root, func(key string) string { return set[key] })
	for _, dir := range []string{host.ConfigDir, host.ManagedDir} {
		if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || filepath.IsAbs(rel) {
			t.Errorf("%q is not inside %q", dir, root)
		}
	}
}

func TestHostFor_TheDefaultDirectoryIsUnreadOnlyWhereTheEnvironmentMovesIt(t *testing.T) {
	none := func(string) string { return "" }
	if _, moved := HostFor("linux", "/home/u", none).UnreadUserSettingsPath(); moved {
		t.Error("UnreadUserSettingsPath() reports a file Claude Code does not read where nothing moved the directory")
	}

	host := HostFor("linux", "/home/u", func(key string) string {
		return map[string]string{ConfigDirEnv: "/elsewhere/claude"}[key]
	})
	path, moved := host.UnreadUserSettingsPath()
	if !moved {
		t.Fatal("UnreadUserSettingsPath() reports nothing where the environment moved the directory")
	}
	if want := filepath.Join("/home/u", ".claude", "settings.json"); path != want {
		t.Errorf("UnreadUserSettingsPath() = %q, want %q", path, want)
	}
	if path == host.UserSettingsPath() {
		t.Error("UnreadUserSettingsPath() names the file Claude Code reads")
	}
}
