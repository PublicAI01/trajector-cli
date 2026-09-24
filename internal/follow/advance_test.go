package follow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		Store: func(res follow.ReadResult) follow.Storing {
			a.handed = append(a.handed, res)
			return a.answer
		},
		ReadAt: at,
	}
	return a
}

func (a *advancing) register(path, subpath string) {
	a.t.Helper()
	if err := a.registry.RegisterUnder(project, path, subpath); err != nil {
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

func (a *advancing) listed() []follow.File {
	a.t.Helper()
	files, err := a.registry.Files(project)
	if err != nil {
		a.t.Fatal(err)
	}
	return files
}

// stored is every entry the registry file holds, listed or not.
func (a *advancing) stored() []map[string]json.RawMessage {
	a.t.Helper()
	raw, err := os.ReadFile(filepath.Join(a.dir, project+".json"))
	if err != nil {
		a.t.Fatal(err)
	}
	var reg struct {
		Files []map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		a.t.Fatal(err)
	}
	return reg.Files
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

func TestReader_RetiresTheEntryOfASessionThatLeftTheProject(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	kept := assistantLine("m1", 0, "hello")
	writeFile(t, path, kept+relocatedLine("/elsewhere/entirely"))
	a.register(path, "")
	a.answer = follow.Stored

	a.reader.Advance(path)

	if files := a.listed(); len(files) != 0 {
		t.Fatalf("registry lists %+v, want nothing left to read", files)
	}
	stored := a.stored()
	if len(stored) != 1 || string(stored[0]["retired"]) != `"relocated"` {
		t.Fatalf("registry file holds %v, want the entry kept with why it stopped", stored)
	}

	a.register(path, "")
	if files := a.listed(); len(files) != 0 {
		t.Errorf("registry lists %+v after the path was registered again, want the file still retired", files)
	}
	if len(a.handed) != 1 || len(segmentLines(t, a.handed[0])) != 1 {
		t.Errorf("reads handed to the store = %+v, want the one segment before the session left", a.handed)
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
		t.Errorf("registry file holds %v, want the entry gone with the file", stored)
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
