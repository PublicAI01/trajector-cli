package follow_test

import (
	"math"
	"os"
	"path/filepath"
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
		{"retired", follow.File{LastEvent: stamp(time.Hour), PID: 42, Retired: follow.Relocated}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.file.Hot(now, alive); got != tc.want {
				t.Errorf("Hot() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSplitPartsTheHotFilesFromTheCold(t *testing.T) {
	now := time.Now()
	files := []follow.File{
		{Path: "/a", LastEvent: now.Format(time.RFC3339)},
		{Path: "/b"},
		{Path: "/c", PID: 1},
	}
	hot, cold := follow.Split(files, now, func(pid int) bool { return pid == 1 })
	if len(hot) != 2 || hot[0].Path != "/a" || hot[1].Path != "/c" {
		t.Errorf("hot = %+v, want /a and /c", hot)
	}
	if len(cold) != 1 || cold[0].Path != "/b" {
		t.Errorf("cold = %+v, want /b", cold)
	}
}

func TestRegistry_WarmAndCoolLeaveTheCursorAlone(t *testing.T) {
	r := follow.Open(t.TempDir())
	path := abs(t, "a.jsonl")
	mustRegister(t, r, project, path)
	advanced := follow.File{Path: path, Inode: 7, Size: 10, Offset: 10, NextSegment: 2, MessageIDs: []string{"msg_a"}}
	if err := r.Update(project, advanced); err != nil {
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

func TestGroupHoldsASessionsFilesAndNoOthers(t *testing.T) {
	main := abs(t, "s.jsonl")
	agent := abs(t, "s/subagents/agent-x.jsonl")
	files := []follow.File{
		{Path: main},
		{Path: agent},
		{Path: abs(t, "s/subagents/agent-x.meta.json")},
		{Path: abs(t, "other.jsonl")},
		{Path: abs(t, "other/subagents/agent-x.jsonl")},
	}
	for _, path := range []string{main, agent} {
		got := follow.Group(files, path)
		if len(got) != 3 || got[0].Path != main {
			t.Errorf("Group(_, %q) = %+v, want the main file and its two agent files", filepath.Base(path), got)
		}
	}
	if got := follow.Group(files, abs(t, "gone.jsonl")); len(got) != 0 {
		t.Errorf("Group(_, an unregistered session) = %+v, want none", got)
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
