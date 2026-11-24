package follow_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// The helpers below spell the on-disk layout a second time, on purpose:
// what these tests hold is the bytes a released build finds on disk,
// and a rename inside the package must not move them.

// digestOf is how the layout names a file after s: the first 16 bytes
// of its SHA-256, in hex.
func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func projectFileOf(dir string) string { return filepath.Join(dir, project+".json") }

func projectDirOf(dir string) string { return filepath.Join(dir, project) }

// entryFileOf is the entry file of path.
func entryFileOf(dir, path string) string {
	return filepath.Join(projectDirOf(dir), digestOf(path)+".json")
}

// sessionFileOf is the file of the session path belongs to with ext.
func sessionFileOf(dir, path, ext string) string {
	return filepath.Join(projectDirOf(dir), digestOf(follow.SessionOf(path))+ext)
}

// keysOf reads the JSON object in file, member by member.
func keysOf(t *testing.T, file string) map[string]jsontext.Value {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]jsontext.Value
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("%s = %s: %v", filepath.Base(file), data, err)
	}
	return obj
}

// setKey writes one member into the JSON object in file, as a build
// that knows the member would.
func setKey(t *testing.T, file, key, value string) {
	t.Helper()
	obj := keysOf(t, file)
	obj[key] = jsontext.Value(value)
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshot is one file as it stood: which file, and its bytes.
type snapshot struct {
	info os.FileInfo
	data []byte
}

func snap(t *testing.T, file string) snapshot {
	t.Helper()
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot{info, data}
}

// same reports whether file is still the file s observed, byte for
// byte.
func (s snapshot) same(t *testing.T, file string) bool {
	t.Helper()
	now := snap(t, file)
	return os.SameFile(s.info, now.info) && bytes.Equal(s.data, now.data)
}

func names(t *testing.T, dir string) (files, dirs []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		} else {
			files = append(files, e.Name())
		}
	}
	return files, dirs
}

func agentOf(main, name string) string {
	return filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", name)
}

func TestLayout_EachEntryHeatAndToldMarkIsAFileInTheProjectDirectory(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	agent := agentOf(main, "agent-a1.jsonl")
	line := assistantLine("m1", 0, "hello")
	writeFile(t, main, line)
	writeFile(t, agent, userLine(1))
	if err := a.registry.RegisterAll(project, []string{main, agent}, "api"); err != nil {
		t.Fatal(err)
	}
	a.answer = follow.Stored
	a.noticed = drift.Signals{AssistantLines: 1, AssistantLinesWithoutMessageID: 1}
	a.reader.Advance(main)
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	if err := a.registry.Warm(project, main, 42, at); err != nil {
		t.Fatal(err)
	}
	if _, err := a.registry.Tell(project, main); err != nil {
		t.Fatal(err)
	}

	files, dirs := names(t, a.dir)
	if !slices.Equal(files, []string{project + ".json"}) || !slices.Equal(dirs, []string{project}) {
		t.Errorf("registry directory holds files %q and directories %q, want the project file and the project directory", files, dirs)
	}
	files, dirs = names(t, projectDirOf(a.dir))
	want := []string{
		filepath.Base(entryFileOf(a.dir, main)),
		filepath.Base(entryFileOf(a.dir, agent)),
		filepath.Base(sessionFileOf(a.dir, main, ".heat")),
		filepath.Base(sessionFileOf(a.dir, main, ".told")),
	}
	slices.Sort(want)
	if !slices.Equal(files, want) || len(dirs) != 0 {
		t.Errorf("project directory holds %q and directories %q, want %q", files, dirs, want)
	}

	proj := keysOf(t, projectFileOf(a.dir))
	if string(proj["version"]) != "2" || len(proj["needs_build"]) < 3 || proj["files"] != nil {
		t.Errorf("project file = %v, want version 2 naming the build it needs, and no files", proj)
	}
	entry := keysOf(t, entryFileOf(a.dir, main))
	if got, err := json.Marshal(main); err != nil || string(entry["path"]) != string(got) {
		t.Errorf("entry path = %s, want %s", entry["path"], got)
	}
	for key, want := range map[string]string{
		"subpath":      `"api"`,
		"next_segment": "1",
		"message_ids":  `["m1"]`,
		"read_at":      `"` + readRunAt + `"`,
		"signals":      `{"assistant_lines":1,"assistant_lines_without_message_id":1}`,
	} {
		if got := string(entry[key]); got != want {
			t.Errorf("entry %s = %s, want %s", key, got, want)
		}
	}
	for _, key := range []string{"last_event", "pid", "told", "retired", "bound", "outside", "needs", "version"} {
		if entry[key] != nil {
			t.Errorf("entry = %v, want no %s", entry, key)
		}
	}
	heat := keysOf(t, sessionFileOf(a.dir, main, ".heat"))
	if len(heat) != 2 || string(heat["last_event"]) != `"2026-11-23T09:00:00Z"` || string(heat["pid"]) != "42" {
		t.Errorf("heat file = %v, want the event time and the process alone", heat)
	}
	if told := snap(t, sessionFileOf(a.dir, main, ".told")); len(told.data) != 0 {
		t.Errorf("told mark holds %q, want an empty file", told.data)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(projectDirOf(a.dir))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("project directory = %v, %v, want mode 700", info, err)
	}
	for _, name := range files {
		if info, err := os.Stat(filepath.Join(projectDirOf(a.dir), name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s = %v, %v, want mode 600", name, info, err)
		}
	}
}

func TestLayout_AnEarlierBuildFindsOnlyAProjectFileOfALayoutItDoesNotKnow(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	writeFile(t, main, userLine(1))
	a.register(main, "")
	a.answer = follow.Stored
	a.reader.Advance(main)
	if err := a.registry.SetGaps(project, follow.Gaps{Truncated: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.Warm(project, main, 7, time.Now()); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	var registries []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			registries = append(registries, e.Name())
		}
	}
	if !slices.Equal(registries, []string{project + ".json"}) {
		t.Fatalf("registry files an earlier build lists = %q, want the project file alone", registries)
	}
	if v := keysOf(t, projectFileOf(a.dir))["version"]; string(v) != "2" {
		t.Errorf("project file version = %s, want 2: an earlier build stops reading any other", v)
	}
}

func TestLayout_AnEntryOutsideIsWrittenAsOutsideAndIsNotHot(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	writeFile(t, main, assistantLine("m1", 0, "hello")+relocatedLine("/elsewhere/entirely"))
	a.register(main, "")
	a.answer = follow.Stored
	a.reader.Advance(main)
	now := time.Now()
	if err := a.registry.Warm(project, main, 0, now); err != nil {
		t.Fatal(err)
	}

	entry := keysOf(t, entryFileOf(a.dir, main))
	if string(entry["outside"]) != "true" || entry["retired"] != nil {
		t.Errorf("entry = %v, want outside true and no retirement", entry)
	}
	hot, err := a.registry.HotFiles(project, now, func(int) bool { return true })
	if err != nil || len(hot) != 0 {
		t.Errorf("HotFiles() = %+v, %v, want an entry outside not hot", hot, err)
	}

	appendFile(t, main, relocatedLine(root)+userLine(2))
	a.reader.Advance(main)
	if entry := keysOf(t, entryFileOf(a.dir, main)); entry["outside"] != nil {
		t.Errorf("entry = %v, want no outside key once the session came back", entry)
	}
}

func TestLayout_AKeyThisBuildDoesNotKnowOutlivesEveryWriter(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	other := filepath.Join(filepath.Dir(main), "sid-other.jsonl")
	writeFile(t, main, userLine(1))
	writeFile(t, other, userLine(1))
	a.register(main, "")
	a.register(other, "")
	const key, value = "a_later_key", `{"kept":true}`
	setKey(t, entryFileOf(a.dir, main), key, value)
	setKey(t, projectFileOf(a.dir), key, value)

	writers := []struct {
		name  string
		write func() error
	}{
		{"a read that stores a segment", func() error {
			a.answer, a.noticed = follow.Stored, drift.Signals{AssistantLines: 1, AssistantLinesWithoutMessageID: 1}
			a.reader.Advance(main)
			return nil
		}},
		{"a read that pauses", func() error {
			appendFile(t, main, userLine(2))
			a.answer, a.noticed = follow.Paused, drift.Signals{IncompleteSegments: 1}
			a.reader.Advance(main)
			return nil
		}},
		{"a read that stores again", func() error {
			a.answer, a.noticed = follow.Stored, drift.Signals{}
			a.reader.Advance(main)
			return nil
		}},
		{"Warm", func() error { return a.registry.Warm(project, main, 42, time.Now()) }},
		{"Tell", func() error { _, err := a.registry.Tell(project, main); return err }},
		{"Cool", func() error { return a.registry.Cool(project, main) }},
		{"SetGaps", func() error { return a.registry.SetGaps(project, follow.Gaps{Truncated: true}) }},
		{"TellReasoning", func() error { _, err := a.registry.TellReasoning(project, time.Now()); return err }},
		{"RegisterAll", func() error {
			return a.registry.RegisterAll(project, []string{main, filepath.Join(filepath.Dir(main), "sid-new.jsonl")}, "")
		}},
		{"a read that drops another entry with signals", func() error {
			a.answer, a.noticed = follow.Stored, drift.Signals{AssistantLines: 2, AssistantLinesWithoutMessageID: 2}
			a.reader.Advance(other)
			if err := os.Remove(other); err != nil {
				return err
			}
			a.reader.Advance(other)
			return nil
		}},
	}
	for _, w := range writers {
		if err := w.write(); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		if got := string(keysOf(t, entryFileOf(a.dir, main))[key]); got != value {
			t.Errorf("after %s: entry %s = %q, want %s as it was found", w.name, key, got, value)
		}
		if got := string(keysOf(t, projectFileOf(a.dir))[key]); got != value {
			t.Errorf("after %s: project file %s = %q, want %s as it was found", w.name, key, got, value)
		}
	}
	if f := entry(t, a.registry, main); f.Offset != int64(len(userLine(1)+userLine(2))) {
		t.Errorf("entry = %+v, want the reads to have moved its cursor all the same", f)
	}
}

func TestLayout_AnEntryThatNeedsWhatThisBuildDoesNotKnowIsLeftAlone(t *testing.T) {
	a := newAdvancing(t)
	marked := mainPath(t)
	other := filepath.Join(filepath.Dir(marked), "sid-other.jsonl")
	writeFile(t, marked, userLine(1))
	writeFile(t, other, userLine(1))
	a.register(marked, "")
	a.register(other, "")
	setKey(t, entryFileOf(a.dir, marked), "needs", `["a-later-layout-feature"]`)
	setKey(t, entryFileOf(a.dir, marked), "signals", `{"assistant_lines":5,"assistant_lines_without_message_id":5}`)
	now := time.Now()
	if err := a.registry.Warm(project, marked, 0, now); err != nil {
		t.Fatal(err)
	}
	before := snap(t, entryFileOf(a.dir, marked))
	a.answer = follow.Stored
	var handed []string
	store := a.reader.Store
	a.reader.Store = func(res follow.ReadResult) (follow.Storing, drift.Signals) {
		handed = append(handed, res.File.Path)
		return store(res)
	}

	a.reader.Advance(marked)
	a.reader.Advance(other)
	if err := a.registry.RegisterAll(project, []string{marked}, "sub"); err != nil {
		t.Fatal(err)
	}
	removeAll(t, []string{marked})
	a.reader.Advance(marked)

	if !slices.Equal(handed, []string{other}) {
		t.Errorf("reads handed to the store = %q, want only the other entry read", handed)
	}
	if !before.same(t, entryFileOf(a.dir, marked)) {
		t.Error("the entry was rewritten or removed, want it exactly as it was found")
	}
	files, err := a.registry.Files(project)
	if err != nil || len(files) != 1 || files[0].Path != other {
		t.Errorf("Files() = %+v, %v, want only the other entry", files, err)
	}
	if got, err := a.registry.SessionFiles(project, marked); err != nil || len(got) != 0 {
		t.Errorf("SessionFiles() = %+v, %v, want none", got, err)
	}
	if hot, err := a.registry.HotFiles(project, now, func(int) bool { return true }); err != nil || len(hot) != 0 {
		t.Errorf("HotFiles() = %+v, %v, want none: the session's only entry needs what this build does not know", hot, err)
	}
	if _, ok, err := a.registry.File(project, marked); err != nil || ok {
		t.Errorf("File() = %v, %v, want not found", ok, err)
	}
	if s, err := a.registry.Signals(project); err != nil || s.AssistantLines != 0 {
		t.Errorf("Signals() = %+v, %v, want nothing of the entry left alone", s, err)
	}

	if err := a.registry.Unregister(project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(projectDirOf(a.dir)); !os.IsNotExist(err) {
		t.Errorf("project directory after Unregister: stat = %v, want it gone with the entry", err)
	}
}

func TestLayout_AnEntryFileThatDoesNotParseIsSkippedAndNeverReadFromTheStart(t *testing.T) {
	a := newAdvancing(t)
	broken := mainPath(t)
	other := filepath.Join(filepath.Dir(broken), "sid-other.jsonl")
	writeFile(t, broken, userLine(1))
	writeFile(t, other, userLine(1))
	a.register(broken, "")
	a.register(other, "")
	if err := os.WriteFile(entryFileOf(a.dir, broken), []byte(`{"path":`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snap(t, entryFileOf(a.dir, broken))
	a.answer = follow.Stored
	var handed []string
	store := a.reader.Store
	a.reader.Store = func(res follow.ReadResult) (follow.Storing, drift.Signals) {
		handed = append(handed, res.File.Path)
		return store(res)
	}

	a.reader.Advance(broken)
	a.reader.Advance(other)
	if err := a.registry.RegisterAll(project, []string{broken}, ""); err != nil {
		t.Errorf("RegisterAll() of the path = %v, want the entry file found and left", err)
	}

	if !slices.Equal(handed, []string{other}) {
		t.Errorf("reads handed to the store = %q, want only the other entry read", handed)
	}
	if !before.same(t, entryFileOf(a.dir, broken)) {
		t.Error("the entry file that does not parse was replaced, want it left as it was")
	}
	files, err := a.registry.Files(project)
	if err != nil || len(files) != 1 || files[0].Path != other {
		t.Errorf("Files() = %+v, %v, want the other entry listed", files, err)
	}
}

func TestLayout_TwoRegistrationsOfOnePathMakeOneEntryAndKeepItsCursor(t *testing.T) {
	a := newAdvancing(t)
	read := mainPath(t)
	fresh := filepath.Join(filepath.Dir(read), "sid-fresh.jsonl")
	writeFile(t, read, userLine(1))
	a.register(read, "")
	a.answer = follow.Stored
	a.reader.Advance(read)
	cursor := entry(t, a.registry, read)

	registries := []*follow.Registry{follow.Open(a.dir), follow.Open(a.dir)}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 16 {
		r := registries[i%2]
		wg.Go(func() {
			if err := r.RegisterAll(project, []string{fresh, read}, "sub"); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("RegisterAll: %v", err)
	}

	files, err := a.registry.Files(project)
	if err != nil || len(files) != 2 {
		t.Fatalf("Files() = %+v, %v, want two entries", files, err)
	}
	if got := entry(t, a.registry, read); !sameFile(got, cursor) || got.Subpath != "" {
		t.Errorf("entry read before = %+v, want its cursor and subpath as they were: %+v", got, cursor)
	}
	left, _ := names(t, projectDirOf(a.dir))
	if len(left) != 2 {
		t.Errorf("project directory holds %q, want the two entry files and nothing a registration left behind", left)
	}
}

func TestLayout_OnlyOneOfManyTellsIsTheFirst(t *testing.T) {
	dir := t.TempDir()
	main := abs(t, "s.jsonl")
	agent := abs(t, "s/subagents/agent-x.jsonl")
	mustRegister(t, follow.Open(dir), project, main, agent)
	registries := []*follow.Registry{follow.Open(dir), follow.Open(dir)}

	var mu sync.Mutex
	firsts := 0
	var wg sync.WaitGroup
	for i := range 16 {
		r := registries[i%2]
		wg.Go(func() {
			first, err := r.Tell(project, main)
			if err != nil {
				t.Error(err)
			}
			if first {
				mu.Lock()
				firsts++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if firsts != 1 {
		t.Errorf("Tell answered first %d times, want once", firsts)
	}
	if first, err := registries[0].Tell(project, agent); err != nil || first {
		t.Errorf("Tell of an agent file of the told session = %v, %v, want told already", first, err)
	}
}

func TestLayout_AWarmFromTheSameProcessWithinAMinuteWritesNothing(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	main := abs(t, "s.jsonl")
	mustRegister(t, r, project, main)
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	heat := sessionFileOf(dir, main, ".heat")
	if err := r.Warm(project, main, 42, at); err != nil {
		t.Fatal(err)
	}

	before := snap(t, heat)
	for _, pid := range []int{42, 0} {
		if err := r.Warm(project, main, pid, at.Add(59*time.Second)); err != nil {
			t.Fatal(err)
		}
		if !before.same(t, heat) {
			t.Errorf("heat file rewritten by a Warm with pid %d within a minute, want it left", pid)
		}
	}
	if err := r.Warm(project, main, 43, at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if string(keysOf(t, heat)["pid"]) != "43" {
		t.Errorf("heat = %v, want a Warm from another process written", keysOf(t, heat))
	}
	if err := r.Warm(project, main, 43, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if string(keysOf(t, heat)["last_event"]) != `"2026-11-23T09:02:00Z"` {
		t.Errorf("heat = %v, want a Warm a minute later written", keysOf(t, heat))
	}

	if err := r.Cool(project, main); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(heat); !os.IsNotExist(err) {
		t.Errorf("heat file after Cool: stat = %v, want it removed", err)
	}
}

func TestLayout_ARoundWritesTheBoundBeforeTheStoreAndTheCursorWithWhatItNoticedAfter(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	line := assistantLine("m1", 0, "hello")
	writeFile(t, main, line)
	a.register(main, "")
	file := entryFileOf(a.dir, main)
	writes := registryWrites(t, file)
	a.noticed = drift.Signals{AssistantLines: 1, AssistantLinesWithoutMessageID: 1}
	var during map[string]jsontext.Value
	a.reader.Store = func(follow.ReadResult) (follow.Storing, drift.Signals) {
		during = keysOf(t, file)
		return follow.Stored, a.noticed
	}

	a.reader.Advance(main)

	if want := `{"offset":0,"segment":0,"end":` + jsonInt(len(line)); !strings.HasPrefix(string(during["bound"]), want) {
		t.Errorf("bound while the store held the segment = %s, want it to start %s", during["bound"], want)
	}
	if string(during["offset"]) != "0" || during["signals"] != nil {
		t.Errorf("entry while the store held the segment = %v, want the cursor unmoved and nothing noticed yet", during)
	}
	after := keysOf(t, file)
	if string(after["offset"]) != jsonInt(len(line)) || after["bound"] != nil || string(after["signals"]) != `{"assistant_lines":1,"assistant_lines_without_message_id":1}` {
		t.Errorf("entry after the round = %v, want the cursor moved with what it noticed and no bound", after)
	}
	if n, counted := writes(); counted && n != 2 {
		t.Errorf("entry written %d times in one round, want twice", n)
	}
}

func jsonInt(n int) string {
	data, err := json.Marshal(n)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestLayout_ACursorMovesWhileAnotherProcessHoldsTheProjectLock(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	line := userLine(1)
	writeFile(t, main, line)
	a.register(main, "")
	unlock, err := fsatomic.Lock(projectFileOf(a.dir), time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	a.answer, a.noticed = follow.Stored, drift.Signals{AssistantLines: 1}

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.reader.Advance(main)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader waited on the project lock")
	}

	if f := entry(t, a.registry, main); f.Offset != int64(len(line)) {
		t.Errorf("cursor = %+v, want it moved past the line", f)
	}
}

func TestLayout_AnEntryThatLeavesKeepsWhatItNoticedInTheProjectFile(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	writeFile(t, main, userLine(1))
	a.register(main, "")
	a.answer, a.noticed = follow.Stored, drift.Signals{AssistantLines: 3, AssistantLinesWithoutMessageID: 1}
	a.reader.Advance(main)
	removeAll(t, []string{main})
	a.answer, a.noticed = follow.Stored, drift.Signals{}

	a.reader.Advance(main)

	if _, err := os.Stat(entryFileOf(a.dir, main)); !os.IsNotExist(err) {
		t.Errorf("entry file after its file was found gone: stat = %v, want it gone", err)
	}
	if got := string(keysOf(t, projectFileOf(a.dir))["signals"]); got != `{"assistant_lines":3,"assistant_lines_without_message_id":1}` {
		t.Errorf("project file signals = %s, want what the removed entry noticed", got)
	}
	if s, err := a.registry.Signals(project); err != nil || s.AssistantLines != 3 {
		t.Errorf("Signals() = %+v, %v, want the count kept", s, err)
	}
}

func TestLayout_FilesOfAKindThisBuildDoesNotKnowAreIgnored(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	main := abs(t, "s.jsonl")
	mustRegister(t, r, project, main)
	for _, name := range []string{"later.kind", "x.json.tmp-1", "y.json.new-1"} {
		if err := os.WriteFile(filepath.Join(projectDirOf(dir), name), []byte(`{"path":"/elsewhere"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	files, err := r.Files(project)
	if err != nil || len(files) != 1 || files[0].Path != main {
		t.Errorf("Files() = %+v, %v, want the one entry", files, err)
	}
}

func TestLayout_AProjectOfAnotherLayoutIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, raw, says string
	}{
		{"version 1", `{"version":1,"files":[]}`, "version 1"},
		{"a later version", `{"version":3,"needs_build":"9.9.9"}`, "9.9.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(projectFileOf(dir), []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			r := follow.Open(dir)
			path := abs(t, "a.jsonl")
			if _, err := r.Files(project); err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("Files() error = %v, want refused naming %q", err, tc.says)
			}
			if err := r.RegisterAll(project, []string{path}, ""); err == nil {
				t.Error("RegisterAll() = nil, want refused")
			}
			if _, err := os.Stat(projectDirOf(dir)); !os.IsNotExist(err) {
				t.Errorf("project directory: stat = %v, want none created", err)
			}
			if got, err := os.ReadFile(projectFileOf(dir)); err != nil || string(got) != tc.raw {
				t.Errorf("project file = %q, %v, want left as %q", got, err, tc.raw)
			}
		})
	}
}

func TestLayout_AnEntryFileThatIsThereIsNeverReplacedByANewOne(t *testing.T) {
	target := filepath.Join(t.TempDir(), "entry.json")
	writeFile(t, target, `{"path":"/s/a.jsonl","offset":40,"next_segment":2}`)
	before := snap(t, target)

	if err := follow.PlaceNew(target, []byte(`{"path":"/s/a.jsonl"}`)); err != nil {
		t.Fatalf("PlaceNew() = %v, want an entry already there to count as placed", err)
	}

	if !before.same(t, target) {
		t.Error("the entry file was replaced, want the one that was there kept with its cursor")
	}
	if left, _ := filepath.Glob(target + ".*"); len(left) != 0 {
		t.Errorf("files left beside the entry: %q, want none", left)
	}
}

func TestLayout_AnAgentFileIsNotReadWhileItsMainEntryNeedsWhatThisBuildDoesNotKnow(t *testing.T) {
	a := newAdvancing(t)
	main := mainPath(t)
	agent := agentPath(main, "subagents")
	writeFile(t, main, userLine(1))
	writeFile(t, agent, userLine(2))
	a.register(main, "")
	a.register(agent, "")
	setKey(t, entryFileOf(a.dir, main), "needs", `["a-later-layout-feature"]`)
	before := snap(t, entryFileOf(a.dir, agent))
	a.answer = follow.Stored
	handed := 0
	store := a.reader.Store
	a.reader.Store = func(res follow.ReadResult) (follow.Storing, drift.Signals) {
		handed++
		return store(res)
	}

	if !a.reader.Advance(agent) {
		t.Error("Advance = false, want the project's next entry still read")
	}

	if handed != 0 {
		t.Errorf("%d reads of the agent file were handed to the store, want none: whether its session is outside is in an entry this build may not read", handed)
	}
	if !before.same(t, entryFileOf(a.dir, agent)) {
		t.Error("the agent file's entry was rewritten, want its cursor where it was")
	}
}
