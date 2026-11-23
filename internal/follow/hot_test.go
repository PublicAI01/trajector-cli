package follow_test

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

// entry is one registered file of the project, by path.
func entry(t *testing.T, r *follow.Registry, path string) follow.File {
	t.Helper()
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%q is not registered", path)
	return follow.File{}
}

func TestFile_HotWhileItsProcessRunsOrAHookNamedItWithinADay(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	alive := func(pid int) bool { return pid == 42 }
	stamp := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	for _, tc := range []struct {
		name string
		file follow.File
		want bool
	}{
		{"never named by a hook", follow.File{}, false},
		{"named an hour ago", follow.File{LastEvent: stamp(time.Hour)}, true},
		{"named two days ago", follow.File{LastEvent: stamp(48 * time.Hour)}, false},
		{"named two days ago but its process runs", follow.File{LastEvent: stamp(48 * time.Hour), PID: 42}, true},
		{"its process is gone and it was named two days ago", follow.File{LastEvent: stamp(48 * time.Hour), PID: 7}, false},
		{"retired", follow.File{LastEvent: stamp(time.Hour), PID: 42, Retired: "a-reason-of-a-later-build"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.file.Hot(now, alive); got != tc.want {
				t.Errorf("Hot() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFile_LeftToReadWhenItGrewWasReplacedOrIsGone(t *testing.T) {
	read := follow.File{Inode: 7, Size: 100, Offset: 100}
	for _, tc := range []struct {
		name string
		stat follow.Stat
		want bool
	}{
		{"unchanged", follow.Stat{Exists: true, Inode: 7, Size: 100}, false},
		{"grew", follow.Stat{Exists: true, Inode: 7, Size: 150}, true},
		{"grew on a system with no inode", follow.Stat{Exists: true, Size: 150}, true},
		{"replaced", follow.Stat{Exists: true, Inode: 8, Size: 100}, true},
		{"shrank", follow.Stat{Exists: true, Inode: 7, Size: 40}, true},
		{"gone", follow.Stat{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := read.LeftToRead(tc.stat); got != tc.want {
				t.Errorf("LeftToRead() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRegistry_HotFilesListsTheFilesOfHotSessionsOnly(t *testing.T) {
	r := follow.Open(t.TempDir())
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	named := abs(t, "named.jsonl")
	namedAgent := abs(t, "named/subagents/agent-x.jsonl")
	quiet := abs(t, "quiet.jsonl")
	running := abs(t, "running.jsonl")
	gone := abs(t, "gone.jsonl")
	mustRegister(t, r, project, named, namedAgent, quiet, running, gone)
	for path, pid := range map[string]int{named: 0, running: 1, gone: 7} {
		at := now.Add(-48 * time.Hour)
		if path == named {
			at = now.Add(-time.Hour)
		}
		if err := r.Warm(project, path, pid, at); err != nil {
			t.Fatal(err)
		}
	}

	hot, err := r.HotFiles(project, now, func(pid int) bool { return pid == 1 })
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, f := range hot {
		got = append(got, f.Path)
	}
	if want := []string{named, namedAgent, running}; !slices.Equal(got, want) {
		t.Errorf("HotFiles() = %q, want %q", got, want)
	}
}

func TestRegistry_WarmAndCoolLeaveTheCursorAlone(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	advanced := follow.File{Path: path, Inode: 7, Size: 10, Offset: 10, NextSegment: 2, MessageIDs: []string{"msg_a"}}
	if err := r.Update(project, follow.File{Path: path}, advanced); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	if err := r.Warm(project, path, 42, at); err != nil {
		t.Fatal(err)
	}
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !sameFile(files[0], advanced) {
		t.Fatalf("Files() after Warm = %+v, want the cursor untouched", files)
	}
	if files[0].PID != 42 || files[0].LastEvent != "2026-09-19T12:00:00Z" {
		t.Errorf("Files() after Warm = %+v, want pid 42 and the event time", files[0])
	}
	if !files[0].Hot(at, func(int) bool { return false }) {
		t.Error("a file just named by a hook is not hot")
	}

	if err := r.Warm(project, path, 0, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	files, _ = r.Files(project)
	if files[0].PID != 42 || files[0].LastEvent != "2026-09-19T12:01:00Z" {
		t.Errorf("Files() after a Warm without a pid = %+v, want the pid kept and the time moved", files[0])
	}

	if err := r.Cool(project, path); err != nil {
		t.Fatal(err)
	}
	files, _ = r.Files(project)
	if len(files) != 1 || !sameFile(files[0], advanced) || files[0].PID != 0 || files[0].LastEvent != "" {
		t.Errorf("Files() after Cool = %+v, want the cursor kept and the heat gone", files)
	}
	if files[0].Hot(at, func(int) bool { return true }) {
		t.Error("a cooled file with no process is hot")
	}
}

func TestFile_QuietForCountsFromTheLastHookEvent(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		file follow.File
		want time.Duration
	}{
		{"named just now", follow.File{LastEvent: now.Format(time.RFC3339)}, 0},
		{"named an hour ago", follow.File{LastEvent: now.Add(-time.Hour).Format(time.RFC3339)}, time.Hour},
		{"never named by a hook", follow.File{}, math.MaxInt64},
		{"a time not in the layout's form", follow.File{LastEvent: "yesterday"}, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.file.QuietFor(now); got != tc.want {
				t.Errorf("QuietFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRegistry_WarmAndCoolMarkTheWholeSession(t *testing.T) {
	r := follow.Open(t.TempDir())
	main := abs(t, "s.jsonl")
	agent := abs(t, "s/subagents/agent-x.jsonl")
	meta := abs(t, "s/subagents/agent-x.meta.json")
	elsewhere := abs(t, "other.jsonl")
	mustRegister(t, r, project, main, agent, meta, elsewhere)
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	if err := r.Warm(project, main, 42, at); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{main: true, agent: true, meta: true, elsewhere: false} {
		f := entry(t, r, path)
		if hot := f.LastEvent != "" && f.PID == 42; hot != want {
			t.Errorf("%s after Warm of the main file = %+v, want hot %v", filepath.Base(path), f, want)
		}
	}

	if err := r.Cool(project, main); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{main, agent, meta} {
		if f := entry(t, r, path); f.LastEvent != "" || f.PID != 0 {
			t.Errorf("%s after Cool of the main file = %+v, want cold", filepath.Base(path), f)
		}
	}
	if files, _ := r.Files(project); len(files) != 4 {
		t.Errorf("files = %d, want the four registered: marking a session registers nothing", len(files))
	}
}

func TestRegistry_WarmOfAnAgentFileMarksItsMainFile(t *testing.T) {
	r := follow.Open(t.TempDir())
	main := abs(t, "s.jsonl")
	agent := abs(t, "s/subagents/agent-x.jsonl")
	mustRegister(t, r, project, main, agent)

	if err := r.Warm(project, agent, 42, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if f := entry(t, r, main); f.LastEvent != "2026-09-19T12:00:00Z" || f.PID != 42 {
		t.Errorf("main file after Warm of an agent file = %+v, want hot", f)
	}
}

func TestRegistry_SessionFilesHoldsASessionsFilesAndNoOthers(t *testing.T) {
	r := follow.Open(t.TempDir())
	main := abs(t, "s.jsonl")
	agent := abs(t, "s/subagents/agent-x.jsonl")
	meta := abs(t, "s/subagents/agent-x.meta.json")
	inRun := abs(t, "s/subagents/workflows/wf_1/agent-y.jsonl")
	mustRegister(t, r, project, main, agent, meta, inRun,
		abs(t, "other.jsonl"),
		abs(t, "other/subagents/agent-x.jsonl"),
		abs(t, "other/subagents/workflows/wf_1/agent-y.jsonl"))
	want := []string{main, agent, meta, inRun}
	slices.Sort(want)

	for _, path := range []string{main, agent, inRun} {
		files, err := r.SessionFiles(project, path)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range files {
			got = append(got, f.Path)
		}
		if !slices.Equal(got, want) {
			t.Errorf("SessionFiles(_, %q) = %q, want the main file and its three agent files", filepath.Base(path), got)
		}
	}
	if got, err := r.SessionFiles(project, abs(t, "gone.jsonl")); err != nil || len(got) != 0 {
		t.Errorf("SessionFiles(_, an unregistered session) = %+v, %v, want none", got, err)
	}
}

func TestRegistry_FileIsTheEntryFilesListsForAPath(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	path := abs(t, "a.jsonl")
	retired := abs(t, "b.jsonl")
	mustRegister(t, r, project, path, retired)
	retireInFile(t, dir, retired, "a-reason-of-a-later-build")
	advanced := follow.File{Path: path, Inode: 7, Size: 10, Offset: 10, NextSegment: 2, MessageIDs: []string{"msg_a"}}
	if err := r.Update(project, follow.File{Path: path}, advanced); err != nil {
		t.Fatal(err)
	}

	f, ok, err := r.File(project, path)
	if err != nil || !ok || !sameFile(f, advanced) {
		t.Errorf("File(_, a) = %+v, %v, %v, want the advanced entry", f, ok, err)
	}
	if _, ok, err := r.File(project, abs(t, "c.jsonl")); err != nil || ok {
		t.Errorf("File(_, an unregistered path) = %v, %v, want not found", ok, err)
	}
	if _, ok, err := r.File(project, retired); err != nil || ok {
		t.Errorf("File(_, an entry Files does not list) = %v, %v, want not found", ok, err)
	}
	if _, ok, err := follow.Open(t.TempDir()).File(project, path); err != nil || ok {
		t.Errorf("File() of a project with no registry = %v, %v, want not found", ok, err)
	}
}

func TestSessionOf_APathThatIsNeitherMainNorAgentFileIsItsOwnSession(t *testing.T) {
	for _, name := range []string{
		"s/notes.txt",
		"s/subagents/x.jsonl.bak",
		"s/subagents/deeper/agent-x.meta.json",
		"s/subagents/workflows/run-1/agent-y.meta.json",
	} {
		t.Run(name, func(t *testing.T) {
			path := abs(t, name)
			if got := follow.SessionOf(path); got != path {
				t.Errorf("SessionOf(%q) = %q, want the path itself", path, got)
			}
		})
	}
}

func TestRegistry_WarmRefusesAPathThatIsNotRegistered(t *testing.T) {
	r := follow.Open(t.TempDir())
	if err := r.Warm(project, abs(t, "a.jsonl"), 1, time.Now()); err == nil {
		t.Error("Warm of an unregistered path = nil, want refused")
	}
	mustRegister(t, r, project, abs(t, "a.jsonl"))
	if err := r.Cool(project, abs(t, "b.jsonl")); err == nil {
		t.Error("Cool of an unregistered path = nil, want refused")
	}
}

func TestProcessAliveSeesThisProcessAndNotAnImpossibleOne(t *testing.T) {
	if !follow.ProcessAlive(os.Getpid()) {
		t.Error("ProcessAlive(this process) = false")
	}
	for _, pid := range []int{0, -1} {
		if follow.ProcessAlive(pid) {
			t.Errorf("ProcessAlive(%d) = true", pid)
		}
	}
}
