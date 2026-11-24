package follow_test

import (
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
)

// advancing is one project's registry, a reader over it, and what its
// store was handed. answer is what the store reports for the next
// read.
type advancing struct {
	t        *testing.T
	dir      string
	registry *follow.Registry
	reader   follow.Reader
	answer   follow.Storing
	noticed  drift.Signals
	handed   []follow.ReadResult
}

const readRunAt = "2026-09-11T10:00:00Z"

func newAdvancing(t *testing.T) *advancing {
	t.Helper()
	dir := t.TempDir()
	a := &advancing{t: t, dir: dir, registry: follow.Open(dir)}
	at, err := time.Parse(time.RFC3339, readRunAt)
	if err != nil {
		t.Fatal(err)
	}
	a.reader = follow.Reader{
		Registry:      a.registry,
		ProjectIDHash: project,
		Options:       follow.ReadOptions{Root: root},
		Capture:       capture,
		Store: func(res follow.ReadResult) (follow.Storing, drift.Signals) {
			a.handed = append(a.handed, res)
			return a.answer, a.noticed
		},
		ReadAt: at,
	}
	return a
}

func (a *advancing) register(path, subpath string) {
	a.t.Helper()
	if err := a.registry.RegisterAll(project, []string{path}, subpath); err != nil {
		a.t.Fatal(err)
	}
}

// entry is the registered entry for path, and fails the test when the
// registry no longer lists one.
func (a *advancing) entry(path string) follow.File {
	a.t.Helper()
	files, err := a.registry.Files(project)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	a.t.Fatalf("registry = %+v, want an entry for %q", files, path)
	return follow.File{}
}

// stored is every entry file the project directory holds, listed or
// not, member by member.
func (a *advancing) stored() []map[string]jsontext.Value {
	a.t.Helper()
	files, err := filepath.Glob(filepath.Join(projectDirOf(a.dir), "*.json"))
	if err != nil {
		a.t.Fatal(err)
	}
	var entries []map[string]jsontext.Value
	for _, file := range files {
		entries = append(entries, keysOf(a.t, file))
	}
	return entries
}

func TestReader_AdvancesTheCursorOnlyOnceEveryRecordIsStored(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	line := assistantLine("m1", 0, "hello")
	writeFile(t, path, line)
	a.register(path, "")

	a.answer = follow.Held
	if a.reader.Advance(path) {
		t.Error("Advance with nothing stored = true, want the project's reading held")
	}
	if f := a.entry(path); f.Offset != 0 || f.NextSegment != 0 || f.ReadAt != "" {
		t.Fatalf("cursor after nothing was stored = %+v, want it left where it was", f)
	}

	a.answer = follow.Stored
	if !a.reader.Advance(path) {
		t.Error("Advance after the records were stored = false, want reading to go on")
	}
	f := a.entry(path)
	if f.Offset != int64(len(line)) || f.NextSegment != 1 || f.ReadAt != readRunAt {
		t.Errorf("cursor after the records were stored = %+v, want it past the line", f)
	}
	if len(a.handed) != 2 {
		t.Fatalf("reads handed to the store = %d, want the same bytes offered twice", len(a.handed))
	}
	first, second := segmentLines(t, a.handed[0]), segmentLines(t, a.handed[1])
	if len(second) != 1 || len(first) != len(second) || first[0] != second[0] {
		t.Errorf("lines handed = %v then %v, want the held read offered again unchanged", first, second)
	}
}

func TestReader_LeavesTheRegistryUntouchedWhenNoEntryGainedAnything(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	paths := []string{main}
	writeFile(t, main, userLine(1))
	for i := range 157 {
		agent := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", fmt.Sprintf("agent-a%d.jsonl", i))
		writeFile(t, agent, userLine(1))
		paths = append(paths, agent)
	}
	if err := a.registry.RegisterAll(project, paths, ""); err != nil {
		t.Fatal(err)
	}
	a.answer = follow.Stored
	for _, path := range paths {
		a.reader.Advance(path)
	}
	written := []string{projectFileOf(a.dir)}
	for _, path := range paths {
		written = append(written, entryFileOf(a.dir, path))
	}
	before := make([]snapshot, len(written))
	for i, file := range written {
		before[i] = snap(t, file)
	}

	a.reader.ReadAt = a.reader.ReadAt.Add(time.Hour)
	for _, path := range paths {
		a.reader.Advance(path)
	}

	rewritten := 0
	for i, file := range written {
		if !before[i].same(t, file) {
			rewritten++
		}
	}
	if rewritten != 0 {
		t.Errorf("%d registry files were rewritten by %d entries that gained nothing, want each left as it was", rewritten, len(paths))
	}
	if f := a.entry(main); f.ReadAt != readRunAt {
		t.Errorf("read_at = %q, want the time of the read that last found something, %q", f.ReadAt, readRunAt)
	}
}

func TestReader_AFileNothingCouldBeStoredForLeavesTheNextOneToRead(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, assistantLine("m1", 0, "hello"))
	a.register(path, "")

	a.answer = follow.NotStored
	if !a.reader.Advance(path) {
		t.Error("Advance = false, want the project's next entry still read")
	}
	if f := a.entry(path); f.Offset != 0 || f.NextSegment != 0 {
		t.Errorf("cursor = %+v, want it left where it was", f)
	}
}

func TestReader_ReadsOnAfterASessionLeftTheProject(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	kept := assistantLine("m1", 0, "hello")
	writeFile(t, path, kept+relocatedLine("/elsewhere/entirely"))
	a.register(path, "")
	a.answer = follow.Stored

	a.reader.Advance(path)

	f := a.entry(path)
	if !f.Outside {
		t.Fatalf("entry = %+v, want it listed and outside", f)
	}
	if stored := a.stored(); len(stored) != 1 || string(stored[0]["outside"]) != "true" || stored[0]["retired"] != nil {
		t.Errorf("project directory holds %v, want the entry written as outside", stored)
	}
	a.register(path, "")
	if again := a.entry(path); again.Offset != f.Offset || !again.Outside {
		t.Errorf("entry after the path was registered again = %+v, want it as it was", again)
	}

	appendFile(t, path, userLine(1)+relocatedLine(root)+userLine(2))
	a.reader.Advance(path)

	var got []string
	for _, res := range a.handed {
		got = append(got, segmentLines(t, res)...)
	}
	if want := []string{kept, userLine(2)}; !slices.Equal(got, want) {
		t.Errorf("lines handed to the store = %q, want %q", got, want)
	}
	if f := a.entry(path); f.Outside {
		t.Errorf("entry = %+v, want it inside after the session came back", f)
	}
	if stored := a.stored(); len(stored) != 1 || stored[0]["outside"] != nil {
		t.Errorf("project directory holds %v, want the entry no longer outside once the session came back", stored)
	}
}

func TestReader_DropsTheEntryOfAFileThatVanished(t *testing.T) {
	a := newAdvancing(t)
	path := filepath.Join(t.TempDir(), "gone.jsonl")
	a.register(path, "")
	a.answer = follow.Stored

	if !a.reader.Advance(path) {
		t.Error("Advance = false, want the project's next entry still read")
	}
	if stored := a.stored(); len(stored) != 0 {
		t.Errorf("project directory holds %v, want the entry gone with the file", stored)
	}
}

func TestReader_TakesTheHeatAndTheToldMarkOfASessionWithItsLastEntry(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	agent := agentOf(main, "agent-a1.jsonl")
	writeFile(t, main, userLine(1))
	writeFile(t, agent, userLine(1))
	a.register(main, "")
	a.register(agent, "")
	if err := a.registry.Warm(project, main, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.registry.Tell(project, main); err != nil {
		t.Fatal(err)
	}
	a.answer = follow.Stored
	marks := func() (heat, told bool) {
		_, heatErr := os.Stat(sessionFileOf(a.dir, main, ".heat"))
		_, toldErr := os.Stat(sessionFileOf(a.dir, main, ".told"))
		return heatErr == nil, toldErr == nil
	}

	removeAll(t, []string{agent})
	a.reader.Advance(agent)
	if heat, told := marks(); !heat || !told {
		t.Errorf("heat, told mark after the reader dropped one entry of two = %v, %v, want both kept", heat, told)
	}

	removeAll(t, []string{main})
	a.reader.Advance(main)
	if stored := a.stored(); len(stored) != 0 {
		t.Errorf("project directory holds %v, want the last entry of the session gone with its file", stored)
	}
	if heat, told := marks(); heat || told {
		t.Errorf("heat, told mark after the reader dropped the session's last entry = %v, %v, want both gone with it", heat, told)
	}
}

func TestReader_KeepsTheEntryOfAFileWhoseDirectoryIsOutOfReach(t *testing.T) {
	a := newAdvancing(t)
	path := filepath.Join(t.TempDir(), "unmounted", "back-later.jsonl")
	a.register(path, "")
	before := snap(t, entryFileOf(a.dir, path))
	a.answer = follow.Stored

	if !a.reader.Advance(path) {
		t.Error("Advance = false, want the project's next entry still read")
	}

	if !before.same(t, entryFileOf(a.dir, path)) {
		t.Error("the entry was removed or rewritten, want it kept as it was: the file may come back with its directory")
	}
}

func TestReader_KeepsTheCursorOfAFileItCouldNotRead(t *testing.T) {
	a := newAdvancing(t)
	path := filepath.Join(t.TempDir(), sessionID, "subagents", "agent-a.meta.json")
	writeFile(t, path, `{"broken`)
	a.register(path, "")
	a.answer = follow.Stored

	if !a.reader.Advance(path) {
		t.Error("Advance over a file that could not be read = false, want reading to go on")
	}
	if len(a.handed) != 0 {
		t.Errorf("reads handed to the store = %+v, want none", a.handed)
	}
	if f := a.entry(path); f.Offset != 0 || f.ReadAt != "" {
		t.Errorf("cursor = %+v, want it left for the next run", f)
	}
}

func TestReader_GivesEachRecordThePositionOfTheEntryItCameFrom(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, assistantLine("m1", 0, "hello"))
	a.register(path, "api/worker")
	a.answer = follow.Stored

	a.reader.Advance(path)

	if len(a.handed) != 1 || len(a.handed[0].Segments) != 1 {
		t.Fatalf("reads handed to the store = %+v, want one segment", a.handed)
	}
	got := a.handed[0].Segments[0].Capture
	if got.ProjectSubpath != "api/worker" {
		t.Errorf("record position = %q, want where the session ran", got.ProjectSubpath)
	}
	if got.ClientVersion != capture.ClientVersion || got.Injection != capture.Injection {
		t.Errorf("record capture = %+v, want the rest as the run stated it", got)
	}
}

func TestReader_KeepsTheHeatAHookWroteWhileTheFileWasRead(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"cursor advanced", assistantLine("m1", 0, "hello")},
		{"session left", assistantLine("m1", 0, "hello") + relocatedLine("/elsewhere/entirely")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdvancing(t)
			path := mainPath(t)
			writeFile(t, path, tc.content)
			a.register(path, "")
			at := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
			a.reader.Store = func(follow.ReadResult) (follow.Storing, drift.Signals) {
				if err := a.registry.Warm(project, path, 42, at); err != nil {
					t.Fatal(err)
				}
				return follow.Stored, drift.Signals{}
			}

			a.reader.Advance(path)

			stored := a.stored()
			if len(stored) != 1 {
				t.Fatalf("project directory holds %v, want one entry", stored)
			}
			if string(stored[0]["offset"]) != strconv.Itoa(len(tc.content)) {
				t.Errorf("offset = %s, want the cursor past what was read", stored[0]["offset"])
			}
			if f := a.entry(path); f.PID != 42 || f.LastEvent != at.Format(time.RFC3339) {
				t.Errorf("entry = %+v, want the heat the hook wrote while the file was read", f)
			}
		})
	}
}

func TestReader_ReadsOnFromAnEntryLeftOutside(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	kept := assistantLine("m1", 0, "hello")
	out := relocatedLine("/elsewhere/entirely")
	writeFile(t, path, kept+out)
	a.register(path, "")
	left := follow.File{Path: path, Size: int64(len(kept + out)), Offset: int64(len(kept + out)), NextSegment: 1, MessageIDs: []string{"m1"}, Outside: true}
	if err := follow.MoveCursor(a.registry, project, follow.File{Path: path}, left); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, userLine(1)+relocatedLine(root)+userLine(2))
	a.answer = follow.Stored

	a.reader.Advance(path)

	if len(a.handed) != 1 {
		t.Fatalf("reads handed to the store = %d, want one", len(a.handed))
	}
	wantLines(t, a.handed[0], userLine(2))
	stored := a.stored()
	if len(stored) != 1 || stored[0]["outside"] != nil {
		t.Errorf("project directory holds %v, want the entry no longer outside", stored)
	}
}
