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
	ok, err := registered(r, path)
	if err != nil || !ok {
		t.Errorf("Registered() = %v, %v, want true", ok, err)
	}
	ok, err = registered(r, abs(t, "other.jsonl"))
	if err != nil || ok {
		t.Errorf("Registered(other) = %v, %v, want false", ok, err)
	}
}

func TestRegistry_RegisterKeepsExistingCursors(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	advanced := follow.File{Path: path, Inode: 7, Size: 10, Offset: 10, NextSegment: 2, MessageIDs: []string{"msg_a"}}
	if err := follow.MoveCursor(r, project, follow.File{Path: path}, advanced); err != nil {
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
		if err := r.Warm(hash, path, 0, time.Now()); err == nil {
			t.Errorf("Warm(%q) = nil, want refused", hash)
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
			t.Helper()
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
			ok, err := registered(r, abs(t, "a.jsonl"))
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

func TestRegistry_AnAgentFileOfAWorkflowRunIsListedAndHotWithItsSession(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	main := abs(t, "sid.jsonl")
	flat := abs(t, filepath.Join("sid", "subagents", "agent-a.jsonl"))
	inRun := abs(t, filepath.Join("sid", "subagents", "workflows", "wf_1", "agent-b.jsonl"))
	inRunMeta := abs(t, filepath.Join("sid", "subagents", "workflows", "wf_1", "agent-b.meta.json"))
	if err := r.RegisterAll(project, []string{main, flat, inRun, inRunMeta}, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := r.Warm(project, main, 0, now); err != nil {
		t.Fatal(err)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("Files() = %+v, want every entry listed", files)
	}
	for _, f := range files {
		if !f.Hot(now, func(int) bool { return false }) {
			t.Errorf("%s = %+v, want hot with its session", filepath.Base(f.Path), f)
		}
		if got, want := f.KeptFromEarlierBuilds(), f.Path == inRun || f.Path == inRunMeta; got != want {
			t.Errorf("%s: KeptFromEarlierBuilds() = %v, want %v", filepath.Base(f.Path), got, want)
		}
		if entry := keysOf(t, entryFileOf(dir, f.Path)); entry["retired"] != nil {
			t.Errorf("%s on disk = %v, want no retirement", filepath.Base(f.Path), entry)
		}
	}
}

func TestRegistry_RegisterAllWritesNothingWhenEveryPathIsRegistered(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	paths := []string{abs(t, "a.jsonl"), abs(t, "b.jsonl")}
	if err := r.RegisterAll(project, paths, "sub"); err != nil {
		t.Fatal(err)
	}
	written := []string{projectFileOf(dir), entryFileOf(dir, paths[0]), entryFileOf(dir, paths[1])}
	before := make([]snapshot, len(written))
	for i, file := range written {
		before[i] = snap(t, file)
	}

	if err := r.RegisterAll(project, paths, "other"); err != nil {
		t.Fatal(err)
	}

	for i, file := range written {
		if !before[i].same(t, file) {
			t.Errorf("%s was replaced, want nothing written for paths already registered", filepath.Base(file))
		}
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

	if _, err := os.Stat(projectFileOf(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("project file: stat error = %v, want it not created", err)
	}
}

func TestRegistry_ACursorWriteFromACursorThatMovedChangesNothing(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	start := follow.File{Path: path}
	moved := follow.File{Path: path, Inode: 7, Size: 10, Offset: 10, NextSegment: 1, MessageIDs: []string{"msg_a"}}
	if err := follow.MoveCursor(r, project, start, moved); err != nil {
		t.Fatal(err)
	}

	late := follow.File{Path: path, Inode: 7, Size: 30, Offset: 30, NextSegment: 1, MessageIDs: []string{"msg_b"}}
	if err := follow.MoveCursor(r, project, start, late); err == nil {
		t.Error("a cursor write from a cursor that moved = nil, want an error")
	}
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !sameFile(files[0], moved) {
		t.Errorf("Files() = %+v, want the entry as the first update left it", files)
	}
}

func TestRegistry_ACursorWriteReplacesTheCursor(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	a, b := abs(t, "a.jsonl"), abs(t, "b.jsonl")
	mustRegister(t, r, project, a, b)
	first := follow.File{Path: a, Inode: 11, Size: 300, Offset: 300, NextSegment: 1, MessageIDs: []string{"msg_a", "msg_b"}}
	if err := follow.MoveCursor(r, project, follow.File{Path: a}, first); err != nil {
		t.Fatal(err)
	}
	second := follow.File{Path: a, Inode: 12, Size: 40, Offset: 40, NextSegment: 2, MessageIDs: []string{"msg_c"}}
	if err := follow.MoveCursor(r, project, first, second); err != nil {
		t.Fatal(err)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !sameFile(files[0], second) || !sameFile(files[1], follow.File{Path: b}) {
		t.Errorf("Files() = %+v, want [%+v {Path:%s}]", files, second, b)
	}

	raw, err := os.ReadFile(entryFileOf(dir, a))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Path        string   `json:"path"`
		Inode       uint64   `json:"inode"`
		Size        int64    `json:"size"`
		Offset      int64    `json:"offset"`
		NextSegment int      `json:"next_segment"`
		MessageIDs  []string `json:"message_ids"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("entry file %s: %v", raw, err)
	}
	if stored.Path != a || stored.Inode != 12 || stored.Size != 40 || stored.Offset != 40 ||
		stored.NextSegment != 2 || !slices.Equal(stored.MessageIDs, []string{"msg_c"}) {
		t.Errorf("stored entry = %+v, want %+v", stored, second)
	}
	if got := string(keysOf(t, entryFileOf(dir, b))["message_ids"]); got != "[]" {
		t.Errorf("stored zero-cursor entry message_ids = %s, want an empty array", got)
	}
}

func TestReader_AdvanceOfAPathNotRegisteredRegistersNothing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, r *follow.Registry)
	}{
		{name: "directory never created", setup: func(*testing.T, *follow.Registry) {}},
		{name: "project has no registry", setup: func(t *testing.T, r *follow.Registry) {
			t.Helper()
			mustRegister(t, r, "other", abs(t, "a.jsonl"))
		}},
		{name: "path not in the registry", setup: func(t *testing.T, r *follow.Registry) {
			t.Helper()
			mustRegister(t, r, project, abs(t, "b.jsonl"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := follow.Open(t.TempDir())
			tt.setup(t, r)
			path := mainPath(t)
			writeFile(t, path, userLine(1))
			reader := readerWith(r, func(follow.ReadResult) follow.Storing {
				t.Error("the store was handed a read of a path not registered")
				return follow.Stored
			})
			if !reader.Advance(path) {
				t.Error("Advance() = false, want the project's next entry still read")
			}
			if ok, _ := registered(r, path); ok {
				t.Error("Advance of a path not registered registered it")
			}
		})
	}
}

func TestRegistry_UnregisterRemovesProjectFile(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	mustRegister(t, r, project, abs(t, "a.jsonl"))
	mustRegister(t, r, "other", abs(t, "a.jsonl"))
	file := projectFileOf(dir)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("project file before Unregister: %v", err)
	}

	for i := range 2 {
		if err := r.Unregister(project); err != nil {
			t.Fatalf("Unregister() #%d = %v, want nil", i+1, err)
		}
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("project file after Unregister: stat = %v, want not exist", err)
	}
	if _, err := os.Stat(projectDirOf(dir)); !os.IsNotExist(err) {
		t.Errorf("project directory after Unregister: stat = %v, want not exist", err)
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
				t.Helper()
				mustRegister(t, r, "bbb", abs(t, "a.jsonl"))
				mustRegister(t, r, "aaa", abs(t, "a.jsonl"))
				mustRegister(t, r, "ccc", abs(t, "a.jsonl"))
			},
			want: []string{"aaa", "bbb", "ccc"},
		},
		{
			name: "only registry files count",
			setup: func(t *testing.T, dir string, r *follow.Registry) {
				t.Helper()
				mustRegister(t, r, "aaa", abs(t, "a.jsonl"))
				for _, name := range []string{"aaa.json.lock", "aaa.json.tmp-123", "notes.txt"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
						t.Fatalf("write %s: %v", name, err)
					}
				}
				if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o700); err != nil {
					t.Fatalf("mkdir sub.json: %v", err)
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
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	if err := follow.MoveCursor(r, project, follow.File{}, follow.File{Path: path, Offset: 1}); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{dir, projectDirOf(dir)} {
		dirInfo, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s mode = %o, want 700", filepath.Base(d), perm)
		}
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp") || strings.Contains(e.Name(), ".new") || strings.HasSuffix(e.Name(), ".lock") {
				t.Errorf("transient file left behind: %s", e.Name())
			}
		}
	}
	for _, file := range []string{projectFileOf(dir), entryFileOf(dir, path)} {
		fileInfo, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fileInfo.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(file), perm)
		}
	}
}

func TestRegistry_UnknownVersionIsAnError(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "newer version", raw: `{"version":3}`},
		{name: "earlier version", raw: `{"version":1,"files":[]}`},
		{name: "missing version", raw: `{"files":[]}`},
		{name: "not json", raw: `{"version":2,`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(projectFileOf(dir), []byte(tt.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			r := follow.Open(dir)
			path := abs(t, "a.jsonl")
			if _, err := r.Files(project); err == nil {
				t.Error("Files() = nil error, want refused")
			}
			if _, err := registered(r, path); err == nil {
				t.Error("Registered() = nil error, want refused")
			}
			if err := r.RegisterAll(project, []string{path}, ""); err == nil {
				t.Error("RegisterAll() = nil, want refused")
			}
			if _, _, err := r.File(project, path); err == nil {
				t.Error("File() = nil error, want refused")
			}
			got, err := os.ReadFile(projectFileOf(dir))
			if err != nil || string(got) != tt.raw {
				t.Errorf("project file = %q, %v, want left as %q", got, err, tt.raw)
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
		for g := range perRegistry {
			var paths []string
			for i := range perGoroutine {
				paths = append(paths, abs(t, filepath.Join("r"+string(rune('0'+ri)), "g"+string(rune('0'+g)), "f"+string(rune('0'+i))+".jsonl")))
			}
			want = append(want, paths...)
			wg.Go(func() {
				for _, p := range paths {
					if err := r.RegisterAll(project, []string{p}, ""); err != nil {
						errs <- err
					}
				}
			})
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
func registered(r *follow.Registry, path string) (bool, error) {
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
