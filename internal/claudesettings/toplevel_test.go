package claudesettings

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClassifySetting_ChainPrecedenceDecidesStateAndSource(t *testing.T) {
	tests := []struct {
		name         string
		projectLocal string
		project      string
		user         string
		writtenByUs  bool
		want         SettingStatus
	}{
		{
			name: "nothing set anywhere",
			want: SettingStatus{State: Unset},
		},
		{
			name: "true in user settings",
			user: `{"showThinkingSummaries": true}`,
			want: SettingStatus{State: OnByUser, Source: SourceUser},
		},
		{
			name:    "true in project settings beats false in user settings",
			project: `{"showThinkingSummaries": true}`,
			user:    `{"showThinkingSummaries": false}`,
			want:    SettingStatus{State: OnByUser, Source: SourceProject},
		},
		{
			name:         "false in project local beats true in project settings",
			projectLocal: `{"showThinkingSummaries": false}`,
			project:      `{"showThinkingSummaries": true}`,
			want:         SettingStatus{State: OffByUser, Source: SourceProjectLocal},
		},
		{
			name:         "project local true recorded as ours",
			projectLocal: `{"showThinkingSummaries": true}`,
			writtenByUs:  true,
			want:         SettingStatus{State: OnByUs},
		},
		{
			name:         "project local true not recorded as ours",
			projectLocal: `{"showThinkingSummaries": true}`,
			want:         SettingStatus{State: OnByUser, Source: SourceProjectLocal},
		},
		{
			name:        "our record cannot claim a value in another layer",
			user:        `{"showThinkingSummaries": true}`,
			writtenByUs: true,
			want:        SettingStatus{State: OnByUser, Source: SourceUser},
		},
		{
			name:         "project local false recorded as ours is the user's later word",
			projectLocal: `{"showThinkingSummaries": false}`,
			writtenByUs:  true,
			want:         SettingStatus{State: OffByUser, Source: SourceProjectLocal},
		},
		{
			name:         "non-boolean value is skipped",
			projectLocal: `{"showThinkingSummaries": "yes"}`,
			user:         `{"showThinkingSummaries": true}`,
			want:         SettingStatus{State: OnByUser, Source: SourceUser},
		},
		{
			name:         "unreadable layer is skipped",
			projectLocal: `{not json`,
			user:         `{"showThinkingSummaries": false}`,
			want:         SettingStatus{State: OffByUser, Source: SourceUser},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, home := t.TempDir(), t.TempDir()
			if tt.projectLocal != "" {
				writeFileAt(t, ProjectLocalPath(project), tt.projectLocal)
			}
			if tt.project != "" {
				writeFileAt(t, projectSharedPath(project), tt.project)
			}
			if tt.user != "" {
				writeFileAt(t, UserSettingsPath(home), tt.user)
			}
			got := ClassifySetting(project, home, KeyShowThinkingSummaries, tt.writtenByUs)
			if got != tt.want {
				t.Errorf("ClassifySetting = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestClassifySetting_ExplicitFalseIsNotUnset(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	writeFileAt(t, UserSettingsPath(home), `{"other": true}`)
	if got := ClassifySetting(project, home, KeyShowThinkingSummaries, false); got.State != Unset {
		t.Errorf("absent key classified as %+v, want Unset", got)
	}
	writeFileAt(t, UserSettingsPath(home), `{"showThinkingSummaries": false}`)
	want := SettingStatus{State: OffByUser, Source: SourceUser}
	if got := ClassifySetting(project, home, KeyShowThinkingSummaries, false); got != want {
		t.Errorf("explicit false classified as %+v, want %+v", got, want)
	}
}

func TestTopLevelBool_SeparatesExplicitFalseFromAbsent(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantValue bool
		wantFound bool
	}{
		{name: "missing file"},
		{name: "empty object", content: `{}`},
		{name: "key absent among other content", content: `{"env": {"A": "b"}}`},
		{name: "explicit false", content: `{"showThinkingSummaries": false}`, wantFound: true},
		{name: "explicit true", content: `{"showThinkingSummaries": true}`, wantValue: true, wantFound: true},
		{name: "non-boolean value", content: `{"showThinkingSummaries": "true"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.local.json")
			if tt.content != "" {
				writeFileAt(t, path, tt.content)
			}
			value, found := TopLevelBool(path, KeyShowThinkingSummaries)
			if value != tt.wantValue || found != tt.wantFound {
				t.Errorf("TopLevelBool = %v, %v; want %v, %v", value, found, tt.wantValue, tt.wantFound)
			}
		})
	}
}

func TestSetTopLevelBool_PreservesUserContent(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	writeFileAt(t, path, `{
		"permissions": {"allow": ["Bash(npm test)"]},
		"env": {"MY_VAR": "keep-me"},
		"cleanupPeriodDays": 20
	}`)

	if err := SetTopLevelBool(path, KeyShowThinkingSummaries, true); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, path)
	if settings[KeyShowThinkingSummaries] != true {
		t.Errorf("key = %v, want true", settings[KeyShowThinkingSummaries])
	}
	env, _ := settings["env"].(map[string]any)
	if env["MY_VAR"] != "keep-me" {
		t.Errorf("env = %v", settings["env"])
	}
	if _, ok := settings["permissions"]; !ok {
		t.Error("permissions block lost")
	}
	if settings["cleanupPeriodDays"] != float64(20) {
		t.Errorf("cleanupPeriodDays = %v, want kept", settings["cleanupPeriodDays"])
	}
}

func TestSetThenRestore_AbsentKeyIsDeletedAgain(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	writeFileAt(t, path, `{"env": {"MY_VAR": "keep-me"}}`)
	original := readJSON(t, path)

	priorValue, priorFound := TopLevelBool(path, KeyShowThinkingSummaries)
	if err := SetTopLevelBool(path, KeyShowThinkingSummaries, true); err != nil {
		t.Fatal(err)
	}
	if err := RestoreTopLevelBool(path, KeyShowThinkingSummaries, true, priorValue, priorFound); err != nil {
		t.Fatal(err)
	}

	if got := readJSON(t, path); !reflect.DeepEqual(got, original) {
		t.Errorf("settings after set+restore differ from original:\n%v", got)
	}
}

func TestSetThenRestore_ExplicitFalseComesBack(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	writeFileAt(t, path, `{"showThinkingSummaries": false, "env": {"MY_VAR": "keep-me"}}`)
	original := readJSON(t, path)

	priorValue, priorFound := TopLevelBool(path, KeyShowThinkingSummaries)
	if err := SetTopLevelBool(path, KeyShowThinkingSummaries, true); err != nil {
		t.Fatal(err)
	}
	if err := RestoreTopLevelBool(path, KeyShowThinkingSummaries, true, priorValue, priorFound); err != nil {
		t.Fatal(err)
	}

	if got := readJSON(t, path); !reflect.DeepEqual(got, original) {
		t.Errorf("settings after set+restore differ from original:\n%v", got)
	}
}

func TestRestore_UserEditedValueIsLeftAlone(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		priorValue bool
		priorFound bool
	}{
		{
			name:    "flipped to false after our write, absent before",
			content: `{"showThinkingSummaries": false}`,
		},
		{
			name:    "key deleted after our write, absent before",
			content: `{"env": {"MY_VAR": "keep-me"}}`,
		},
		{
			name:       "put back to false by hand, false before",
			content:    `{"showThinkingSummaries": false}`,
			priorFound: true,
		},
		{
			name:       "key deleted after our write, false before",
			content:    `{"other": true}`,
			priorFound: true,
		},
		{
			name:    "replaced with a non-boolean after our write",
			content: `{"showThinkingSummaries": "always"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.local.json")
			writeFileAt(t, path, tt.content)

			if err := RestoreTopLevelBool(path, KeyShowThinkingSummaries, true, tt.priorValue, tt.priorFound); err != nil {
				t.Fatal(err)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, []byte(tt.content)) {
				t.Errorf("restore rewrote a file it had no claim on:\n%s", after)
			}
		})
	}
}

func TestRestore_MissingFileIsLeftMissing(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	if err := RestoreTopLevelBool(path, KeyShowThinkingSummaries, true, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("restore created %s", path)
	}
}
