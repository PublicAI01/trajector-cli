package follow_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

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
		if err := r.Register(projectIDHash, p); err != nil {
			t.Fatalf("Register(%q, %q): %v", projectIDHash, p, err)
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
	if err := r.Update(project, advanced); err != nil {
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
		if err := r.Register(project, path); err == nil {
			t.Errorf("Register(%q) = nil, want refused", path)
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
		if err := r.Register(hash, path); err == nil {
			t.Errorf("Register(%q) = nil, want refused", hash)
		}
		if err := r.Update(hash, follow.File{Path: path}); err == nil {
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

func TestRegistry_EntriesListsRetiredFilesThatFilesLeavesOut(t *testing.T) {
	r := follow.Open(t.TempDir())
	a, b := abs(t, "a.jsonl"), abs(t, "b.jsonl")
	mustRegister(t, r, project, a, b)
	if err := r.Retire(project, follow.File{Path: b}, follow.Relocated); err != nil {
		t.Fatal(err)
	}

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
	if entries[1].Retired != follow.Relocated {
		t.Errorf("Entries()[1] = %+v, want the retirement it was given", entries[1])
	}
}

func TestRegistry_UpdateReplacesWholeEntry(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	a, b := abs(t, "a.jsonl"), abs(t, "b.jsonl")
	mustRegister(t, r, project, a, b)
	first := follow.File{Path: a, Inode: 11, Size: 300, Offset: 300, NextSegment: 1, MessageIDs: []string{"msg_a", "msg_b"}}
	if err := r.Update(project, first); err != nil {
		t.Fatal(err)
	}
	second := follow.File{Path: a, Inode: 12, Size: 40, Offset: 40, NextSegment: 2, MessageIDs: []string{"msg_c"}}
	if err := r.Update(project, second); err != nil {
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
			err := r.Update(project, follow.File{Path: abs(t, "a.jsonl"), Offset: 5})
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
	if err := r.Update(project, follow.File{Path: abs(t, "a.jsonl"), Offset: 1}); err != nil {
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
			if err := r.Register(project, path); err == nil {
				t.Error("Register() = nil, want refused")
			}
			if err := r.Update(project, follow.File{Path: path}); err == nil {
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
					if err := r.Register(project, p); err != nil {
						errs <- err
					}
				}
			}(r, paths)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Register: %v", err)
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
