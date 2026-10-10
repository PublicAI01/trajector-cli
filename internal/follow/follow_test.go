package follow_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

const project = "0123abcd"

func abs(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(string(filepath.Separator), "work", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sameFile(a, b follow.File) bool {
	return a.Path == b.Path && a.Inode == b.Inode && a.Size == b.Size &&
		a.Offset == b.Offset && a.NextSegment == b.NextSegment &&
		slices.Equal(a.MessageIDs, b.MessageIDs)
}

func mustRegister(t *testing.T, r *follow.Registry, projectIDHash string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := r.RegisterAll(projectIDHash, []string{p}, ""); err != nil {
			t.Fatalf("RegisterAll(%q, %q): %v", projectIDHash, p, err)
		}
	}
}

func TestRegistry_RegisterIsIdempotent(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path, path)

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !sameFile(files[0], follow.File{Path: path}) {
		t.Errorf("Files() = %+v, want one zero-cursor entry for %q", files, path)
	}
	ok, err := registered(r, project, path)
	if err != nil || !ok {
		t.Errorf("Registered() = %v, %v, want true", ok, err)
	}
	ok, err = registered(r, project, abs(t, "other.jsonl"))
	if err != nil || ok {
		t.Errorf("Registered(other) = %v, %v, want false", ok, err)
	}
}

func TestRegistry_RegisterKeepsExistingCursors(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	advanced := follow.File{Path: path, Inode: 7, Size: 10, Offset: 10, NextSegment: 2, MessageIDs: []string{"msg_a"}}
	if err := r.Update(project, follow.File{Path: path}, advanced); err != nil {
		t.Fatal(err)
	}
	mustRegister(t, r, project, path)

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !sameFile(files[0], advanced) {
		t.Errorf("Files() = %+v, want %+v untouched", files, advanced)
	}
}

func TestRegistry_RegisterRejectsRelativePath(t *testing.T) {
	r := follow.Open(t.TempDir())
	for _, path := range []string{"", "a.jsonl", "./a.jsonl", "../a.jsonl"} {
		if err := r.RegisterAll(project, []string{path}, ""); err == nil {
			t.Errorf("RegisterAll(%q) = nil, want refused", path)
		}
	}
	projects, err := r.Projects()
	if err != nil || len(projects) != 0 {
		t.Errorf("Projects() = %v, %v, want none", projects, err)
	}
}

func TestRegistry_RejectsProjectHashesThatCouldEscapeTheDirectory(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	for _, hash := range []string{"", ".", "..", ".hidden", "a/b", `a\b`} {
		if err := r.RegisterAll(hash, []string{path}, ""); err == nil {
			t.Errorf("RegisterAll(%q) = nil, want refused", hash)
		}
		if err := r.Update(hash, follow.File{Path: path}, follow.File{Path: path}); err == nil {
			t.Errorf("Update(%q) = nil, want refused", hash)
		}
		if err := r.Unregister(hash); err == nil {
			t.Errorf("Unregister(%q) = nil, want refused", hash)
		}
		if _, err := r.Files(hash); err == nil {
			t.Errorf("Files(%q) = nil error, want refused", hash)
		}
	}
}

func TestRegistry_FilesOfUnknownProjectIsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, r *follow.Registry)
	}{
		{name: "directory never created", setup: func(*testing.T, *follow.Registry) {}},
		{name: "other project registered", setup: func(t *testing.T, r *follow.Registry) {
			mustRegister(t, r, "other", abs(t, "a.jsonl"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := follow.Open(t.TempDir())
			tt.setup(t, r)
			files, err := r.Files(project)
			if err != nil {
				t.Fatalf("Files() error: %v", err)
			}
			if files == nil || len(files) != 0 {
				t.Errorf("Files() = %#v, want an empty slice", files)
			}
			ok, err := registered(r, project, abs(t, "a.jsonl"))
			if err != nil || ok {
				t.Errorf("Registered() = %v, %v, want false", ok, err)
			}
		})
	}
}

func TestRegistry_FilesAreOrderedByPath(t *testing.T) {
	r := follow.Open(t.TempDir())
	c, a, b := abs(t, "c.jsonl"), abs(t, "a.jsonl"), abs(t, "b.jsonl")
	mustRegister(t, r, project, c, a, b)
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	if want := []string{a, b, c}; !reflect.DeepEqual(got, want) {
		t.Errorf("Files() paths = %v, want %v", got, want)
	}
}

// retireInFile writes a retirement onto the entry for path the way a
// build that writes it would: straight into the registry file.
func retireInFile(t *testing.T, dir, path string, why follow.Retirement) {
	t.Helper()
	file := filepath.Join(dir, project+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	for _, f := range reg["files"].([]any) {
		if entry := f.(map[string]any); entry["path"] == path {
			entry["retired"] = string(why)
		}
	}
	if data, err = json.Marshal(reg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegistry_EntriesListsRetiredFilesThatFilesLeavesOut(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	a, b := abs(t, "a.jsonl"), abs(t, "b.jsonl")
	mustRegister(t, r, project, a, b)
	const later follow.Retirement = "a-reason-of-a-later-build"
	retireInFile(t, dir, b, later)

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := r.Entries(project)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 || files[0].Path != a {
		t.Errorf("Files() = %+v, want only %q", files, a)
	}
	var got []string
	for _, f := range entries {
		got = append(got, f.Path)
	}
	if want := []string{a, b}; !reflect.DeepEqual(got, want) {
		t.Errorf("Entries() paths = %v, want %v", got, want)
	}
	if entries[1].Retired != later {
		t.Errorf("Entries()[1] = %+v, want the retirement it was given", entries[1])
	}
}

func TestRegistry_ReadsAnEntryAnEarlierBuildRetiredForLeavingAsOutside(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	left := follow.File{Path: path, Inode: 7, Size: 90, Offset: 90, NextSegment: 1, MessageIDs: []string{"msg_a"}}
	if err := r.Update(project, follow.File{Path: path}, left); err != nil {
		t.Fatal(err)
	}
	retireInFile(t, dir, path, "relocated")

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !sameFile(files[0], left) || !files[0].Outside || files[0].Retired != "" {
		t.Errorf("Files() = %+v, want the entry listed outside, with the cursor the earlier build left", files)
	}
}

// retirementsOnDisk reads the retirement each entry of the registry
// file carries, by path, as a build that does not decode the layout
// through this package would see it.
func retirementsOnDisk(t *testing.T, dir string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Files []struct {
			Path    string `json:"path"`
			Retired string `json:"retired"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range reg.Files {
		got[f.Path] = f.Retired
	}
	return got
}

func TestRegistry_AnAgentFileOfAWorkflowRunIsHiddenOnDiskAndReadHere(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	main := abs(t, "sid.jsonl")
	flat := abs(t, filepath.Join("sid", "subagents", "agent-a.jsonl"))
	inRun := abs(t, filepath.Join("sid", "subagents", "workflows", "wf_1", "agent-b.jsonl"))
	inRunMeta := abs(t, filepath.Join("sid", "subagents", "workflows", "wf_1", "agent-b.meta.json"))
	if err := r.RegisterAll(project, []string{main, flat, inRun, inRunMeta}, ""); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{main: "", flat: "", inRun: "nested-agent-file", inRunMeta: "nested-agent-file"}
	if got := retirementsOnDisk(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("retirements on disk = %v, want %v", got, want)
	}
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("Files() = %+v, want every entry listed", files)
	}
	now := time.Now()
	if err := r.Warm(project, main, 0, now); err != nil {
		t.Fatal(err)
	}
	if files, err = r.Files(project); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Retired != "" || !f.Hot(now, func(int) bool { return false }) {
			t.Errorf("%s = %+v, want read here as an entry not retired, hot with its session", filepath.Base(f.Path), f)
		}
		if got, want := f.KeptFromEarlierBuilds(), f.Path == inRun || f.Path == inRunMeta; got != want {
			t.Errorf("%s: KeptFromEarlierBuilds() = %v, want %v", filepath.Base(f.Path), got, want)
		}
	}

	read := follow.File{Path: inRun, Inode: 3, Size: 40, Offset: 40, NextSegment: 1, MessageIDs: []string{"msg_b"}}
	if err := r.Update(project, follow.File{Path: inRun}, read); err != nil {
		t.Fatal(err)
	}
	if got := retirementsOnDisk(t, dir)[inRun]; got != "nested-agent-file" {
		t.Errorf("retirement on disk after a read = %q, want it kept", got)
	}
}

func TestRegistry_AnAgentFileOfAWorkflowRunOutsideIsWrittenAsRelocated(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	inRun := abs(t, filepath.Join("sid", "subagents", "workflows", "wf_1", "agent-b.jsonl"))
	mustRegister(t, r, project, inRun)
	outside := follow.File{Path: inRun, Offset: 10, Outside: true}
	if err := r.Update(project, follow.File{Path: inRun}, outside); err != nil {
		t.Fatal(err)
	}
	if got := retirementsOnDisk(t, dir)[inRun]; got != "relocated" {
		t.Fatalf("retirement on disk while outside = %q, want relocated", got)
	}
	back := follow.File{Path: inRun, Offset: 20}
	if err := r.Update(project, outside, back); err != nil {
		t.Fatal(err)
	}
	if got := retirementsOnDisk(t, dir)[inRun]; got != "nested-agent-file" {
		t.Errorf("retirement on disk after coming back = %q, want nested-agent-file", got)
	}
}

// keepsARetirementThroughEveryWrite registers marked beside another
// file, writes why onto marked as the build that knows the value would,
// and checks after each kind of write this build makes that marked is
// still carried with why on disk and is not listed.
func keepsARetirementThroughEveryWrite(t *testing.T, marked string, why follow.Retirement) {
	t.Helper()
	dir := t.TempDir()
	r := follow.Open(dir)
	other := abs(t, "other.jsonl")
	mustRegister(t, r, project, other, marked)
	retireInFile(t, dir, marked, why)

	writes := []struct {
		name  string
		write func() error
	}{
		{"Warm of another entry", func() error { return r.Warm(project, other, 0, time.Now()) }},
		{"Cool of the entry", func() error { return r.Cool(project, marked) }},
		{"SetGaps", func() error { return r.SetGaps(project, follow.Gaps{Truncated: true}) }},
		{"RegisterAll of a new path", func() error {
			return r.RegisterAll(project, []string{marked, abs(t, "new.jsonl")}, "")
		}},
	}
	for _, w := range writes {
		if err := w.write(); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		if got := retirementsOnDisk(t, dir)[marked]; got != string(why) {
			t.Errorf("after %s: retirement on disk = %q, want %q as it was found", w.name, got, why)
		}
		files, err := r.Files(project)
		if err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(files, func(f follow.File) bool { return f.Path == marked }) {
			t.Errorf("after %s: Files() lists %s, want it left out", w.name, filepath.Base(marked))
		}
	}
}

func TestRegistry_ARetirementThisBuildDoesNotKnowIsWrittenBackAsFound(t *testing.T) {
	keepsARetirementThroughEveryWrite(t, abs(t, "b.jsonl"), "future")
}

func TestRegistry_AMarkedEntryAtAPlaceThisBuildDoesNotKnowStaysHidden(t *testing.T) {
	unknownPlace := abs(t, filepath.Join("sid", "subagents", "workflows", "run-1", "agent-x.jsonl"))
	keepsARetirementThroughEveryWrite(t, unknownPlace, "nested-agent-file")
}

func TestRegistry_RegisterAllWritesNothingWhenEveryPathIsRegistered(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	paths := []string{abs(t, "a.jsonl"), abs(t, "b.jsonl")}
	if err := r.RegisterAll(project, paths, "sub"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, project+".json")
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.RegisterAll(project, paths, "other"); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the registry file was replaced, want nothing written for paths already registered")
	}
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Subpath != "sub" || files[1].Subpath != "sub" {
		t.Errorf("Files() = %+v, want both entries with the subpath of their first registration", files)
	}
}

func TestRegistry_RegisterAllOfNoPathCreatesNoRegistry(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)

	if err := r.RegisterAll(project, nil, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, project+".json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("registry file: stat error = %v, want it not created", err)
	}
}

func TestRegistry_UpdateFromACursorThatMovedChangesNothing(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	start := follow.File{Path: path}
	moved := follow.File{Path: path, Inode: 7, Size: 10, Offset: 10, NextSegment: 1, MessageIDs: []string{"msg_a"}}
	if err := r.Update(project, start, moved); err != nil {
		t.Fatal(err)
	}

	late := follow.File{Path: path, Inode: 7, Size: 30, Offset: 30, NextSegment: 1, MessageIDs: []string{"msg_b"}}
	if err := r.Update(project, start, late); err == nil {
		t.Error("Update from a cursor that moved = nil, want an error")
	}
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !sameFile(files[0], moved) {
		t.Errorf("Files() = %+v, want the entry as the first update left it", files)
	}
}

func TestRegistry_UpdateReplacesTheCursor(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	a, b := abs(t, "a.jsonl"), abs(t, "b.jsonl")
	mustRegister(t, r, project, a, b)
	first := follow.File{Path: a, Inode: 11, Size: 300, Offset: 300, NextSegment: 1, MessageIDs: []string{"msg_a", "msg_b"}}
	if err := r.Update(project, follow.File{Path: a}, first); err != nil {
		t.Fatal(err)
	}
	second := follow.File{Path: a, Inode: 12, Size: 40, Offset: 40, NextSegment: 2, MessageIDs: []string{"msg_c"}}
	if err := r.Update(project, first, second); err != nil {
		t.Fatal(err)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !sameFile(files[0], second) || !sameFile(files[1], follow.File{Path: b}) {
		t.Errorf("Files() = %+v, want [%+v {Path:%s}]", files, second, b)
	}

	raw, err := os.ReadFile(filepath.Join(dir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Version int `json:"version"`
		Files   []struct {
			Path        string   `json:"path"`
			Inode       uint64   `json:"inode"`
			Size        int64    `json:"size"`
			Offset      int64    `json:"offset"`
			NextSegment int      `json:"next_segment"`
			MessageIDs  []string `json:"message_ids"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("registry file %s: %v", raw, err)
	}
	if stored.Version != 1 || len(stored.Files) != 2 {
		t.Fatalf("registry file = %s, want version 1 with 2 files", raw)
	}
	if got := stored.Files[0]; got.Path != a || got.Inode != 12 || got.Size != 40 || got.Offset != 40 ||
		got.NextSegment != 2 || !slices.Equal(got.MessageIDs, []string{"msg_c"}) {
		t.Errorf("stored entry = %+v, want %+v", got, second)
	}
	if got := stored.Files[1]; got.MessageIDs == nil {
		t.Errorf("stored zero-cursor entry = %s, want message_ids as an array", raw)
	}
}

func TestRegistry_UpdateUnregisteredFails(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, r *follow.Registry)
	}{
		{name: "directory never created", setup: func(*testing.T, *follow.Registry) {}},
		{name: "project has no registry", setup: func(t *testing.T, r *follow.Registry) {
			mustRegister(t, r, "other", abs(t, "a.jsonl"))
		}},
		{name: "path not in the registry", setup: func(t *testing.T, r *follow.Registry) {
			mustRegister(t, r, project, abs(t, "b.jsonl"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := follow.Open(t.TempDir())
			tt.setup(t, r)
			err := r.Update(project, follow.File{}, follow.File{Path: abs(t, "a.jsonl"), Offset: 5})
			if !errors.Is(err, follow.ErrNotRegistered) {
				t.Errorf("Update() = %v, want ErrNotRegistered", err)
			}
			ok, _ := registered(r, project, abs(t, "a.jsonl"))
			if ok {
				t.Error("a failed Update registered the path")
			}
		})
	}
}

func TestRegistry_UnregisterRemovesProjectFile(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	mustRegister(t, r, project, abs(t, "a.jsonl"))
	mustRegister(t, r, "other", abs(t, "a.jsonl"))
	file := filepath.Join(dir, project+".json")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("registry file before Unregister: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := r.Unregister(project); err != nil {
			t.Fatalf("Unregister() #%d = %v, want nil", i+1, err)
		}
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("registry file after Unregister: stat = %v, want not exist", err)
	}
	files, err := r.Files(project)
	if err != nil || len(files) != 0 {
		t.Errorf("Files() after Unregister = %v, %v, want none", files, err)
	}
	projects, err := r.Projects()
	if err != nil || !reflect.DeepEqual(projects, []string{"other"}) {
		t.Errorf("Projects() = %v, %v, want [other]", projects, err)
	}
	if err := follow.Open(filepath.Join(t.TempDir(), "never")).Unregister(project); err != nil {
		t.Errorf("Unregister() without a directory = %v, want nil", err)
	}
}

func TestRegistry_Projects(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string, r *follow.Registry)
		want  []string
	}{
		{
			name:  "directory never created",
			setup: func(*testing.T, string, *follow.Registry) {},
			want:  []string{},
		},
		{
			name: "sorted by hash",
			setup: func(t *testing.T, _ string, r *follow.Registry) {
				mustRegister(t, r, "bbb", abs(t, "a.jsonl"))
				mustRegister(t, r, "aaa", abs(t, "a.jsonl"))
				mustRegister(t, r, "ccc", abs(t, "a.jsonl"))
			},
			want: []string{"aaa", "bbb", "ccc"},
		},
		{
			name: "only registry files count",
			setup: func(t *testing.T, dir string, r *follow.Registry) {
				mustRegister(t, r, "aaa", abs(t, "a.jsonl"))
				for _, name := range []string{"aaa.json.lock", "aaa.json.tmp-123", "notes.txt"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"aaa"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			r := follow.Open(dir)
			tt.setup(t, dir, r)
			got, err := r.Projects()
			if err != nil {
				t.Fatalf("Projects() error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Projects() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRegistry_FilePermissionsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not observable on windows")
	}
	dir := filepath.Join(t.TempDir(), "follow")
	r := follow.Open(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Open created the directory: stat = %v", err)
	}
	mustRegister(t, r, project, abs(t, "a.jsonl"))
	if err := r.Update(project, follow.File{}, follow.File{Path: abs(t, "a.jsonl"), Offset: 1}); err != nil {
		t.Fatal(err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode = %o, want 700", perm)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("registry file mode = %o, want 600", perm)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") || strings.HasSuffix(e.Name(), ".lock") {
			t.Errorf("transient file left behind: %s", e.Name())
		}
	}
}

func TestRegistry_UnknownVersionIsAnError(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "newer version", raw: `{"version":2,"files":[]}`},
		{name: "missing version", raw: `{"files":[]}`},
		{name: "not json", raw: `{"version":1,`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, project+".json"), []byte(tt.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			r := follow.Open(dir)
			path := abs(t, "a.jsonl")
			if _, err := r.Files(project); err == nil {
				t.Error("Files() = nil error, want refused")
			}
			if _, err := registered(r, project, path); err == nil {
				t.Error("Registered() = nil error, want refused")
			}
			if err := r.RegisterAll(project, []string{path}, ""); err == nil {
				t.Error("RegisterAll() = nil, want refused")
			}
			if err := r.Update(project, follow.File{Path: path}, follow.File{Path: path}); err == nil {
				t.Error("Update() = nil, want refused")
			}
			got, err := os.ReadFile(filepath.Join(dir, project+".json"))
			if err != nil || string(got) != tt.raw {
				t.Errorf("registry file = %q, %v, want left as %q", got, err, tt.raw)
			}
		})
	}
}

func TestRegistry_ConcurrentRegistersDoNotLoseEntries(t *testing.T) {
	dir := t.TempDir()
	registries := []*follow.Registry{follow.Open(dir), follow.Open(dir)}
	const perRegistry, perGoroutine = 4, 5

	var wg sync.WaitGroup
	errs := make(chan error, len(registries)*perRegistry*perGoroutine)
	var want []string
	for ri, r := range registries {
		for g := 0; g < perRegistry; g++ {
			var paths []string
			for i := 0; i < perGoroutine; i++ {
				paths = append(paths, abs(t, filepath.Join("r"+string(rune('0'+ri)), "g"+string(rune('0'+g)), "f"+string(rune('0'+i))+".jsonl")))
			}
			want = append(want, paths...)
			wg.Add(1)
			go func(r *follow.Registry, paths []string) {
				defer wg.Done()
				for _, p := range paths {
					if err := r.RegisterAll(project, []string{p}, ""); err != nil {
						errs <- err
					}
				}
			}(r, paths)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("RegisterAll: %v", err)
	}

	files, err := registries[0].Files(project)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Files() has %d paths, want all %d: got %v", len(got), len(want), got)
	}
}

// registered reports whether path is in project's registry, read back
// through Files.
func registered(r *follow.Registry, project, path string) (bool, error) {
	files, err := r.Files(project)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if f.Path == path {
			return true, nil
		}
	}
	return false, nil
}
