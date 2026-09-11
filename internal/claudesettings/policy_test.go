package claudesettings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// policyHost is a temporary host layout: the user's home, the project,
// and the organization's managed directory, all under one root.
type policyHost struct {
	root, home, project, managed string
}

func newPolicyHost(t *testing.T) policyHost {
	t.Helper()
	root := t.TempDir()
	return policyHost{
		root:    root,
		home:    filepath.Join(root, "home"),
		project: filepath.Join(root, "project"),
		managed: filepath.Join(root, "managed"),
	}
}

func (h policyHost) path(rel string) string { return filepath.Join(h.root, filepath.FromSlash(rel)) }

func (h policyHost) judge(getenv func(string) string) HookPolicy {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return JudgeHookPolicy(h.project, h.home, getenv, ManagedDirs{Policy: h.managed})
}

const (
	remoteFile      = "home/.claude/remote-settings.json"
	userFile        = "home/.claude/settings.json"
	projectFile     = "project/.claude/settings.json"
	localFile       = "project/.claude/settings.local.json"
	managedBaseFile = "managed/managed-settings.json"
	shellEnv        = "shell environment"
)

func TestJudgeHookPolicy_EverySourceAndItsRank(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		env   map[string]string
		// wantKey and wantWhere are empty when the hooks run; wantWhere
		// is a path under the host root or the shell environment.
		wantKey, wantWhere string
	}{
		{
			name: "nothing configured anywhere means the hooks run",
		},
		{
			name:      "delivered settings cache disableAllHooks",
			files:     map[string]string{remoteFile: `{"disableAllHooks": true}`},
			wantKey:   "disableAllHooks",
			wantWhere: remoteFile,
		},
		{
			name:      "delivered settings cache allowManagedHooksOnly",
			files:     map[string]string{remoteFile: `{"allowManagedHooksOnly": true}`},
			wantKey:   "allowManagedHooksOnly",
			wantWhere: remoteFile,
		},
		{
			name:      "delivered settings cache strictPluginOnlyCustomization true",
			files:     map[string]string{remoteFile: `{"strictPluginOnlyCustomization": true}`},
			wantKey:   "strictPluginOnlyCustomization",
			wantWhere: remoteFile,
		},
		{
			name:      "strictPluginOnlyCustomization listing hooks locks them",
			files:     map[string]string{remoteFile: `{"strictPluginOnlyCustomization": ["skills", "hooks"]}`},
			wantKey:   "strictPluginOnlyCustomization",
			wantWhere: remoteFile,
		},
		{
			name:  "strictPluginOnlyCustomization listing other areas leaves hooks alone",
			files: map[string]string{remoteFile: `{"strictPluginOnlyCustomization": ["skills"]}`},
		},
		{
			name:  "delivered settings cache false is not a lock",
			files: map[string]string{remoteFile: `{"disableAllHooks": false, "allowManagedHooksOnly": false}`},
		},
		{
			name:      "managed-settings.json disableAllHooks",
			files:     map[string]string{managedBaseFile: `{"disableAllHooks": true}`},
			wantKey:   "disableAllHooks",
			wantWhere: managedBaseFile,
		},
		{
			name:      "managed-settings.json allowManagedHooksOnly",
			files:     map[string]string{managedBaseFile: `{"allowManagedHooksOnly": true}`},
			wantKey:   "allowManagedHooksOnly",
			wantWhere: managedBaseFile,
		},
		{
			name: "delivered settings cache silent on hooks does not shadow a managed file lock",
			files: map[string]string{
				remoteFile:      `{"model": "opus"}`,
				managedBaseFile: `{"disableAllHooks": true}`,
			},
			wantKey:   "disableAllHooks",
			wantWhere: managedBaseFile,
		},
		{
			name: "delivered settings cache is named before the managed file when both lock",
			files: map[string]string{
				remoteFile:      `{"allowManagedHooksOnly": true}`,
				managedBaseFile: `{"disableAllHooks": true}`,
			},
			wantKey:   "allowManagedHooksOnly",
			wantWhere: remoteFile,
		},
		{
			name:      "a drop-in alone decides without a base file",
			files:     map[string]string{"managed/managed-settings.d/hooks.json": `{"disableAllHooks": true}`},
			wantKey:   "disableAllHooks",
			wantWhere: "managed/managed-settings.d/hooks.json",
		},
		{
			name: "a later drop-in overrides an earlier one",
			files: map[string]string{
				"managed/managed-settings.d/10-lock.json":   `{"disableAllHooks": true}`,
				"managed/managed-settings.d/20-unlock.json": `{"disableAllHooks": false}`,
			},
		},
		{
			name: "the later drop-in is the one named",
			files: map[string]string{
				"managed/managed-settings.d/10-unlock.json": `{"disableAllHooks": false}`,
				"managed/managed-settings.d/20-lock.json":   `{"disableAllHooks": true}`,
			},
			wantKey:   "disableAllHooks",
			wantWhere: "managed/managed-settings.d/20-lock.json",
		},
		{
			name: "a drop-in overrides the base file",
			files: map[string]string{
				managedBaseFile:                          `{"disableAllHooks": true}`,
				"managed/managed-settings.d/unlock.json": `{"disableAllHooks": false}`,
			},
		},
		{
			name: "drop-ins override key by key not file by file",
			files: map[string]string{
				managedBaseFile:                         `{"allowManagedHooksOnly": true}`,
				"managed/managed-settings.d/other.json": `{"disableAllHooks": false}`,
			},
			wantKey:   "allowManagedHooksOnly",
			wantWhere: managedBaseFile,
		},
		{
			name: "dot-files and non-json files in the drop-in directory are not read",
			files: map[string]string{
				"managed/managed-settings.d/.hidden.json":  `{"disableAllHooks": true}`,
				"managed/managed-settings.d/notes.txt":     `{"disableAllHooks": true}`,
				"managed/managed-settings.d/lock.json.bak": `{"disableAllHooks": true}`,
			},
		},
		{
			name:      "user settings disableAllHooks",
			files:     map[string]string{userFile: `{"disableAllHooks": true}`},
			wantKey:   "disableAllHooks",
			wantWhere: userFile,
		},
		{
			name:      "project settings disableAllHooks",
			files:     map[string]string{projectFile: `{"disableAllHooks": true}`},
			wantKey:   "disableAllHooks",
			wantWhere: projectFile,
		},
		{
			name:      "local settings disableAllHooks",
			files:     map[string]string{localFile: `{"disableAllHooks": true}`},
			wantKey:   "disableAllHooks",
			wantWhere: localFile,
		},
		{
			name: "local false overrides user true",
			files: map[string]string{
				localFile: `{"disableAllHooks": false}`,
				userFile:  `{"disableAllHooks": true}`,
			},
		},
		{
			name: "project true is named over user true",
			files: map[string]string{
				projectFile: `{"disableAllHooks": true}`,
				userFile:    `{"disableAllHooks": true}`,
			},
			wantKey:   "disableAllHooks",
			wantWhere: projectFile,
		},
		{
			name: "a managed lock is named before a local lock",
			files: map[string]string{
				managedBaseFile: `{"disableAllHooks": true}`,
				localFile:       `{"disableAllHooks": true}`,
			},
			wantKey:   "disableAllHooks",
			wantWhere: managedBaseFile,
		},
		{
			name: "user settings allowManagedHooksOnly is not honored outside managed settings",
			files: map[string]string{
				userFile:  `{"allowManagedHooksOnly": true}`,
				localFile: `{"strictPluginOnlyCustomization": true}`,
			},
		},
		{
			name:      "safe mode in the shell environment",
			env:       map[string]string{"CLAUDE_CODE_SAFE_MODE": "1"},
			wantKey:   "CLAUDE_CODE_SAFE_MODE",
			wantWhere: shellEnv,
		},
		{
			name:      "safe mode spelled TRUE with spaces",
			env:       map[string]string{"CLAUDE_CODE_SAFE_MODE": " TRUE "},
			wantKey:   "CLAUDE_CODE_SAFE_MODE",
			wantWhere: shellEnv,
		},
		{
			name: "safe mode set to 0 is off",
			env:  map[string]string{"CLAUDE_CODE_SAFE_MODE": "0"},
		},
		{
			name: "safe mode set to a spelling Claude Code does not accept is off",
			env:  map[string]string{"CLAUDE_CODE_SAFE_MODE": "enabled"},
		},
		{
			name:      "bare mode in the shell environment",
			env:       map[string]string{"CLAUDE_CODE_SIMPLE": "yes"},
			wantKey:   "CLAUDE_CODE_SIMPLE",
			wantWhere: shellEnv,
		},
		{
			name:      "restricted mode in the shell environment",
			env:       map[string]string{"CLAUDE_CODE_RESTRICTED": "on"},
			wantKey:   "CLAUDE_CODE_RESTRICTED",
			wantWhere: shellEnv,
		},
		{
			name:      "safe mode in the user settings env block",
			files:     map[string]string{userFile: `{"env": {"CLAUDE_CODE_SAFE_MODE": "1"}}`},
			wantKey:   "CLAUDE_CODE_SAFE_MODE",
			wantWhere: userFile,
		},
		{
			name:      "safe mode in the managed file env block",
			files:     map[string]string{managedBaseFile: `{"env": {"CLAUDE_CODE_SAFE_MODE": "true"}}`},
			wantKey:   "CLAUDE_CODE_SAFE_MODE",
			wantWhere: managedBaseFile,
		},
		{
			name: "project and local env blocks cannot set safe mode",
			files: map[string]string{
				projectFile: `{"env": {"CLAUDE_CODE_SAFE_MODE": "1"}}`,
				localFile:   `{"env": {"CLAUDE_CODE_SIMPLE": "1"}}`,
			},
		},
		{
			name:  "user settings env block off overrides the shell",
			files: map[string]string{userFile: `{"env": {"CLAUDE_CODE_SAFE_MODE": "0"}}`},
			env:   map[string]string{"CLAUDE_CODE_SAFE_MODE": "1"},
		},
		{
			name:  "malformed delivered settings cache says nothing",
			files: map[string]string{remoteFile: `{"disableAllHooks": tru`},
		},
		{
			name:  "delivered settings cache that is not an object says nothing",
			files: map[string]string{remoteFile: `[{"disableAllHooks": true}]`},
		},
		{
			name:  "malformed managed file says nothing",
			files: map[string]string{managedBaseFile: `not json`},
		},
		{
			name:  "malformed drop-in says nothing",
			files: map[string]string{"managed/managed-settings.d/broken.json": `{`},
		},
		{
			name:  "malformed user settings say nothing",
			files: map[string]string{userFile: `{"disableAllHooks": true,}`},
		},
		{
			name:  "disableAllHooks that is not a boolean says nothing",
			files: map[string]string{managedBaseFile: `{"disableAllHooks": "true"}`},
		},
		{
			name: "a broken file does not hide a readable lock below it",
			files: map[string]string{
				remoteFile: `{`,
				localFile:  `{"disableAllHooks": true}`,
			},
			wantKey:   "disableAllHooks",
			wantWhere: localFile,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := newPolicyHost(t)
			for rel, content := range tt.files {
				writeFileAt(t, host.path(rel), content)
			}
			got := host.judge(func(key string) string { return tt.env[key] })
			want := HookPolicy{Runs: tt.wantKey == ""}
			if tt.wantKey != "" {
				where := tt.wantWhere
				if where != shellEnv {
					where = host.path(where)
				}
				want.Reason = tt.wantKey + " in " + where
			}
			if got != want {
				t.Errorf("JudgeHookPolicy() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestJudgeHookPolicy_ManagedDirectoryComesFromTheEnvironment(t *testing.T) {
	host := newPolicyHost(t)
	replacement := filepath.Join(host.root, "policy-elsewhere")
	writeFileAt(t, filepath.Join(replacement, "managed-settings.json"), `{"disableAllHooks": true}`)
	writeFileAt(t, host.path(managedBaseFile), `{}`)

	getenv := func(key string) string {
		return map[string]string{"CLAUDE_CODE_MANAGED_SETTINGS_PATH": replacement}[key]
	}
	dir := userdirs.ClaudeManagedSettingsDir(userdirs.Env{GOOS: "linux", Getenv: getenv})
	got := JudgeHookPolicy(host.project, host.home, getenv, ManagedDirs{Policy: dir})
	want := HookPolicy{Reason: "disableAllHooks in " + filepath.Join(replacement, "managed-settings.json")}
	if got != want {
		t.Errorf("JudgeHookPolicy() = %+v, want %+v", got, want)
	}
}

func TestJudgeHookPolicy_NoManagedDirectoryReadsOnlyTheRest(t *testing.T) {
	host := newPolicyHost(t)
	writeFileAt(t, host.path(managedBaseFile), `{"disableAllHooks": true}`)
	if got := JudgeHookPolicy(host.project, host.home, os.Getenv, ManagedDirs{}); !got.Runs {
		t.Errorf("JudgeHookPolicy() = %+v, want the managed file left unread", got)
	}
	writeFileAt(t, host.path(userFile), `{"disableAllHooks": true}`)
	if got := JudgeHookPolicy(host.project, host.home, os.Getenv, ManagedDirs{}); got.Runs {
		t.Errorf("JudgeHookPolicy() = %+v, want the user file read", got)
	}
}

func TestJudgeHookPolicy_EveryCallReadsTheFilesAgain(t *testing.T) {
	host := newPolicyHost(t)
	if got := host.judge(nil); !got.Runs {
		t.Fatalf("JudgeHookPolicy() before any file = %+v", got)
	}
	writeFileAt(t, host.path(localFile), `{"disableAllHooks": true}`)
	if got := host.judge(nil); got.Runs {
		t.Fatalf("JudgeHookPolicy() after writing the lock = %+v", got)
	}
	if err := os.Remove(host.path(localFile)); err != nil {
		t.Fatal(err)
	}
	if got := host.judge(nil); !got.Runs {
		t.Fatalf("JudgeHookPolicy() after removing the lock = %+v", got)
	}
}
