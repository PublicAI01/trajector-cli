package follow_test

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/registryv1"
)

// migrateAndDie migrates the project args[1] of the registry at
// args[0], and is killed as soon as step args[2] of the migration is
// done.
func migrateAndDie(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: migrate-and-die <registry-dir> <project> <step>")
		return 2
	}
	step, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	follow.DieAfterMigrationStep(step)
	fmt.Fprintln(os.Stderr, "migration was not stopped:", follow.Open(args[0]).Migrate(args[1], time.Now(), follow.ProcessAlive))
	return 3
}

// migrateOnce migrates the project args[1] of the registry at args[0]
// and exits 4 when it was refused as pending.
func migrateOnce(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: migrate <registry-dir> <project>")
		return 2
	}
	err := follow.Open(args[0]).Migrate(args[1], time.Now(), follow.ProcessAlive)
	switch {
	case errors.Is(err, follow.ErrMigrationPending):
		return 4
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// earlierFixture is a registry an earlier build left behind: a session
// with an agent file, an agent file of a workflow run and a bound; a
// session that moved outside; an entry retired for a reason no build
// wrote; what the project noticed, the search left uncovered, and the
// day reasoning was told; and the lock of a reader that died.
type earlierFixture struct {
	main, agent, inRun, moved, hidden string
	gaps                              follow.Gaps
	signals                           drift.Signals
}

func newEarlierFixture(t *testing.T) earlierFixture {
	t.Helper()
	sessions := t.TempDir()
	main := filepath.Join(sessions, "sid-a.jsonl")
	fx := earlierFixture{
		main:    main,
		agent:   agentOf(main, "agent-a1.jsonl"),
		inRun:   filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "workflows", "wf_1", "agent-w.jsonl"),
		moved:   filepath.Join(sessions, "sid-b.jsonl"),
		hidden:  filepath.Join(sessions, "sid-c.jsonl"),
		gaps:    follow.Gaps{Unreadable: []string{"/p/unlisted"}},
		signals: drift.Signals{AssistantLines: 5, AssistantLinesWithoutMessageID: 1},
	}
	for _, path := range []string{fx.main, fx.agent, fx.inRun, fx.moved, fx.hidden} {
		writeFile(t, path, userLine(1))
	}
	return fx
}

func (fx earlierFixture) registry() registryv1.Registry {
	return registryv1.Registry{
		Files: []registryv1.Entry{
			{
				Path: fx.main, Inode: 7, Size: 120, Offset: 100, NextSegment: 3,
				MessageIDs: []string{"m1", "m2"}, BeforeRewrite: []string{"m0"},
				Subpath: "sub", ReadAt: "2026-11-20T10:00:00Z", LaunchSurface: "cli",
				EmptyReasoningBlocks: 2, ReasoningBlocksWithText: 1,
				LastEvent: "2026-11-21T09:00:00Z", PID: 11, Told: true,
			},
			{Path: fx.agent, Offset: 40, NextSegment: 1, MessageIDs: []string{"a1"}, LastEvent: "2026-11-21T09:30:00Z", PID: 22, Told: true},
			{Path: fx.inRun, Offset: 10, NextSegment: 1, Retired: "nested-agent-file", LastEvent: "2026-11-21T08:00:00Z", PID: 33, Told: true},
			{Path: fx.moved, Offset: 60, NextSegment: 2, Retired: "relocated"},
			{Path: fx.hidden, Offset: 5, NextSegment: 1, Retired: "kept-for-later"},
		},
		Gaps:            fx.gaps,
		Signals:         fx.signals,
		ReasoningToldOn: "2026-11-21",
	}
}

// writeTo puts the fixture in the registry directory dir.
func (fx earlierFixture) writeTo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, fx.registry()); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.WriteBound(dir, project, fx.main, registryv1.Bound{Offset: 100, Segment: 3, End: 120, Inode: 7}); err != nil {
		t.Fatal(err)
	}
	dead := registryv1.LockPath(dir, project, fx.moved)
	if err := os.WriteFile(dead, []byte("a reader that died"), 0o600); err != nil {
		t.Fatal(err)
	}
	ageLocks(t, dir)
}

// ageLocks ages every lock under dir past the time a live holder may
// hold one, as the lock of a holder that died is once that time has
// passed.
func ageLocks(t *testing.T, dir string) {
	t.Helper()
	long := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".lock") {
			return err
		}
		return os.Chtimes(path, long, long)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// tree is every file under dir by its path below dir, with its bytes.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// migratedTree is the registry directory the fixture becomes after one
// migration that runs through.
func migratedTree(t *testing.T, fx earlierFixture) map[string]string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "follow")
	fx.writeTo(t, dir)
	if err := follow.Open(dir).Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}
	return tree(t, dir)
}

func treeDiff(got, want map[string]string) string {
	var diff strings.Builder
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if g, ok := got[name]; !ok {
			fmt.Fprintf(&diff, "missing %s\n", name)
		} else if g != want[name] {
			fmt.Fprintf(&diff, "%s = %s\nwant    %s\n", name, g, want[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[name]; !ok {
			fmt.Fprintf(&diff, "extra %s = %s\n", name, got[name])
		}
	}
	return diff.String()
}

func TestMigrate_CarriesEachEarlierFieldToItsPlaceInThisLayout(t *testing.T) {
	fx := newEarlierFixture(t)
	dir := filepath.Join(t.TempDir(), "follow")
	fx.writeTo(t, dir)
	r := follow.Open(dir)

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	wantKeys := func(path string, want map[string]string) {
		t.Helper()
		got := keysOf(t, entryFileOf(dir, path))
		for _, gone := range []string{"retired", "last_event", "pid", "told"} {
			if v, ok := got[gone]; ok {
				t.Errorf("entry of %s carries %q = %s, want no such key in this layout", filepath.Base(path), gone, v)
			}
		}
		for key, value := range want {
			if string(got[key]) != value {
				t.Errorf("entry of %s: %q = %s, want %s", filepath.Base(path), key, got[key], value)
			}
		}
		for key := range got {
			if _, ok := want[key]; !ok {
				t.Errorf("entry of %s: %q = %s, want no such key", filepath.Base(path), key, got[key])
			}
		}
	}
	quote := strconv.Quote
	wantKeys(fx.main, map[string]string{
		"path": quote(fx.main), "inode": "7", "size": "120", "offset": "100", "next_segment": "3",
		"message_ids": `["m1","m2"]`, "before_rewrite": `["m0"]`, "subpath": `"sub"`,
		"read_at": `"2026-11-20T10:00:00Z"`, "launch_surface": `"cli"`,
		"empty_reasoning_blocks": "2", "reasoning_blocks_with_text": "1",
		"bound": `{"offset":100,"segment":3,"end":120,"inode":7}`,
	})
	wantKeys(fx.agent, map[string]string{
		"path": quote(fx.agent), "inode": "0", "size": "0", "offset": "40", "next_segment": "1", "message_ids": `["a1"]`,
	})
	wantKeys(fx.inRun, map[string]string{
		"path": quote(fx.inRun), "inode": "0", "size": "0", "offset": "10", "next_segment": "1", "message_ids": `[]`,
	})
	wantKeys(fx.moved, map[string]string{
		"path": quote(fx.moved), "inode": "0", "size": "0", "offset": "60", "next_segment": "2", "message_ids": `[]`, "outside": "true",
	})
	wantKeys(fx.hidden, map[string]string{
		"path": quote(fx.hidden), "inode": "0", "size": "0", "offset": "5", "next_segment": "1", "message_ids": `[]`, "needs": `["kept-for-later"]`,
	})

	if got, err := os.ReadFile(sessionFileOf(dir, fx.main, ".heat")); err != nil || string(got) != `{"last_event":"2026-11-21T09:30:00Z","pid":22}` {
		t.Errorf("heat of the first session = %s, %v, want the latest event of its entries with that event's process", got, err)
	}
	if _, err := os.Stat(sessionFileOf(dir, fx.moved, ".heat")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("heat of a session no hook named: stat = %v, want none", err)
	}
	if _, err := os.Stat(sessionFileOf(dir, fx.main, ".told")); err != nil {
		t.Errorf("told mark of the session told: stat = %v, want it there", err)
	}
	if _, err := os.Stat(sessionFileOf(dir, fx.moved, ".told")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("told mark of a session never told: stat = %v, want none", err)
	}
	if first, err := r.Tell(project, fx.main); err != nil || first {
		t.Errorf("Tell() of the session told = %v, %v, want it told already", first, err)
	}

	projectKeys := keysOf(t, projectFileOf(dir))
	if string(projectKeys["version"]) != "2" || len(projectKeys["needs_build"]) < 3 || string(projectKeys["reasoning_told_on"]) != `"2026-11-21"` {
		t.Errorf("project file = %v, want version 2 naming its build and the day reasoning was told", projectKeys)
	}
	if gaps, err := gapsOf(r); err != nil || !slices.Equal(gaps.Unreadable, fx.gaps.Unreadable) {
		t.Errorf("Contents().Gaps = %+v, %v, want %+v", gaps, err, fx.gaps)
	}
	if signals, err := signalsOf(r); err != nil || signals.AssistantLines != 5 || signals.AssistantLinesWithoutMessageID != 1 {
		t.Errorf("Contents().Signals = %+v, %v, want %+v", signals, err, fx.signals)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, f := range files {
		listed = append(listed, f.Path)
		if f.Path == fx.moved && !f.Outside {
			t.Errorf("entry of the session that moved = %+v, want it outside", f)
		}
	}
	if want := slices.Sorted(slices.Values([]string{fx.main, fx.agent, fx.inRun, fx.moved})); !slices.Equal(listed, want) {
		t.Errorf("Files() = %q, want %q: every entry but the one retired for a reason no build wrote", listed, want)
	}
	if paths, err := registeredPaths(r); err != nil || len(paths) != 5 {
		t.Errorf("Contents().Paths = %q, %v, want all five entries", paths, err)
	}

	for _, gone := range []string{
		registryv1.LockPath(dir, project, fx.moved),
		registryv1.BoundPath(dir, project, fx.main),
		filepath.Join(projectDirOf(dir), "migrating.v1"),
		filepath.Join(projectDirOf(dir), "migrating.v1.lock"),
	} {
		if _, err := os.Stat(gone); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s after the migration: stat = %v, want it gone", filepath.Base(gone), err)
		}
	}
}

func TestMigrate_AMigrationKilledBetweenTwoStepsEndsAsOneThatRanThrough(t *testing.T) {
	fx := newEarlierFixture(t)
	named := []string{fx.main, abs(t, "new.jsonl")}
	hook := func(t *testing.T, r *follow.Registry) (regErr, warmErr error) {
		t.Helper()
		return r.RegisterAll(project, named, ""), r.Warm(project, fx.main, 1, time.Now())
	}
	want := func() map[string]string {
		dir := filepath.Join(t.TempDir(), "follow")
		fx.writeTo(t, dir)
		r := follow.Open(dir)
		if regErr, _ := hook(t, r); !errors.Is(regErr, follow.ErrMigrationPending) {
			t.Fatalf("RegisterAll() before the migration = %v, want ErrMigrationPending", regErr)
		}
		if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
			t.Fatalf("Migrate() = %v", err)
		}
		return tree(t, dir)
	}()
	for step := 1; step <= 5; step++ {
		t.Run(fmt.Sprintf("after step %d", step), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "follow")
			fx.writeTo(t, dir)
			var stderr strings.Builder
			dying := exec.CommandContext(t.Context(), procbin.Self(t, "migrate-and-die"), dir, project, strconv.Itoa(step))
			dying.Stderr = &stderr
			err := dying.Run()
			if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() == 3 || exit.ExitCode() == 2 {
				t.Fatalf("the migrating process = %v, want it killed after step %d\n%s", err, step, stderr.String())
			}
			wantVersion := "1"
			if step >= 4 {
				wantVersion = "2"
			}
			if got := string(keysOf(t, projectFileOf(dir))["version"]); got != wantVersion {
				t.Fatalf("project file version after step %d = %s, want %s", step, got, wantVersion)
			}

			r := follow.Open(dir)
			before := tree(t, dir)
			start := time.Now()
			regErr, warmErr := hook(t, r)
			_, listErr := r.Files(project)
			if took := time.Since(start); took > time.Second {
				t.Errorf("a hook's registration and warming took %v, want them refused without waiting", took)
			}
			for name, err := range map[string]error{"RegisterAll": regErr, "Warm": warmErr, "Files": listErr} {
				if !errors.Is(err, follow.ErrMigrationPending) {
					t.Errorf("%s() = %v, want ErrMigrationPending", name, err)
				}
			}
			for _, path := range named {
				before[filepath.ToSlash(filepath.Join(project, digestOf(path)+".pending"))] = `{"path":` + strconv.Quote(path) + `}`
			}
			if diff := treeDiff(tree(t, dir), before); diff != "" {
				t.Errorf("a refused hook changed the registry but for its pending registrations:\n%s", diff)
			}

			ageLocks(t, dir)
			if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
				t.Fatalf("Migrate() again = %v", err)
			}
			if diff := treeDiff(tree(t, dir), want); diff != "" {
				t.Errorf("registry after a migration killed after step %d and run again differs from one that ran through:\n%s", step, diff)
			}
		})
	}
}

func TestMigrate_OnlyOneOfTwoProcessesMigratesAProject(t *testing.T) {
	fx := newEarlierFixture(t)
	want := migratedTree(t, fx)
	dir := filepath.Join(t.TempDir(), "follow")
	fx.writeTo(t, dir)
	var releases []func()
	for _, e := range fx.registry().Files {
		release, err := registryv1.HoldEntry(dir, project, e.Path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	done := make(chan error, 1)
	go func() { done <- follow.Open(dir).Migrate(project, time.Now(), follow.ProcessAlive) }()
	if !appears(filepath.Join(projectDirOf(dir), "migrating.v1.lock")) {
		t.Fatal("the first migration never took its lock")
	}
	before := tree(t, dir)

	other := exec.CommandContext(t.Context(), procbin.Self(t, "migrate"), dir, project)
	out, err := other.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 4 {
		t.Errorf("the second process's migration = %v, want it refused as pending\n%s", err, out)
	}
	if diff := treeDiff(tree(t, dir), before); diff != "" {
		t.Errorf("the second process changed the registry:\n%s", diff)
	}
	for _, release := range releases {
		release()
	}
	if err := <-done; err != nil {
		t.Fatalf("the first migration = %v", err)
	}
	if diff := treeDiff(tree(t, dir), want); diff != "" {
		t.Errorf("registry after two processes migrated it differs from one migration:\n%s", diff)
	}
}

func TestMigrate_AnEarlierReaderInTheMiddleOfARoundKeepsTheSegmentItStored(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	path := mainPath(t)
	lines := sixLines()
	first := strings.Join(lines[:3], "")
	writeFile(t, path, first)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, registryv1.Registry{Files: []registryv1.Entry{{Path: path}}}); err != nil {
		t.Fatal(err)
	}
	kept := keptOnDisk{dir: t.TempDir()}
	release, err := registryv1.HoldEntry(dir, project, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	kept.store(follow.ReadResult{Segments: []envelope.Segment{{SegmentIndex: 0, Lines: first}}})

	done := make(chan error, 1)
	go func() { done <- follow.Open(dir).Migrate(project, time.Now(), follow.ProcessAlive) }()
	if !appears(filepath.Join(projectDirOf(dir), "migrating.v1.lock")) {
		t.Fatal("the migration never took its lock")
	}
	select {
	case err := <-done:
		t.Fatalf("Migrate() = %v while an earlier reader held an entry, want it to wait for the reader", err)
	case <-time.After(100 * time.Millisecond):
	}
	err = registryv1.Update(dir, project, func(reg *registryv1.Registry) {
		reg.Files[0].Offset, reg.Files[0].Size, reg.Files[0].NextSegment = int64(len(first)), int64(len(first)), 1
	})
	if err != nil {
		t.Fatalf("the earlier reader's cursor write = %v, want the registry still version 1", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	appendFile(t, path, lines[3])
	readerWith(follow.Open(dir), kept.store).Advance(path)

	if got := kept.lines(t); !slices.Equal(got, lines[:4]) {
		t.Errorf("stored lines = %q\nwant every line once: %q", got, lines[:4])
	}
}

// largeRegistryEntries is how many entries the large registry holds:
// the most one device was seen to keep with TRAJECTOR_FULL_SCALE set,
// and a sixth of that otherwise, so the daily run stays quick.
func largeRegistryEntries() int {
	if os.Getenv("TRAJECTOR_FULL_SCALE") != "" {
		return 2400
	}
	return 400
}

func TestMigrate_EveryEntryOfALargeRegistryKeepsItsCursor(t *testing.T) {
	n := largeRegistryEntries()
	dir := filepath.Join(t.TempDir(), "follow")
	reg := registryv1.Registry{}
	for i := range n {
		session := abs(t, fmt.Sprintf("s%03d.jsonl", i/20))
		path := session
		if i%20 != 0 {
			path = agentOf(session, fmt.Sprintf("agent-%02d.jsonl", i%20))
		}
		ids := make([]string, 50)
		for j := range ids {
			ids[j] = fmt.Sprintf("msg_%d_%d", i, j)
		}
		reg.Files = append(reg.Files, registryv1.Entry{
			Path: path, Inode: uint64(i + 1), Size: int64(i*10 + 5), Offset: int64(i * 10), NextSegment: i % 7,
			MessageIDs: ids, EmptyReasoningBlocks: i % 3, ReasoningBlocksWithText: i % 5,
			LastEvent: "2026-11-21T09:00:00Z", PID: 100 + i/20,
		})
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, reg); err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)

	start := time.Now()
	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}
	t.Logf("migrated %d entries in %v", n, time.Since(start))

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]follow.File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if len(byPath) != n {
		t.Fatalf("Files() lists %d entries, want %d", len(byPath), n)
	}
	for _, e := range reg.Files {
		f := byPath[e.Path]
		if f.Inode != e.Inode || f.Size != e.Size || f.Offset != e.Offset || f.NextSegment != e.NextSegment ||
			!slices.Equal(f.MessageIDs, e.MessageIDs) ||
			f.EmptyReasoningBlocks != e.EmptyReasoningBlocks || f.ReasoningBlocksWithText != e.ReasoningBlocksWithText ||
			f.LastEvent != e.LastEvent || f.PID != e.PID {
			t.Fatalf("entry of %s = %+v, want what the earlier entry held: %+v", e.Path, f, e)
		}
	}
}

func TestMigrate_EmptiesAProjectDirectoryLeftBesideAnEarlierProjectFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	left, kept := abs(t, "left.jsonl"), abs(t, "kept.jsonl")
	r := follow.Open(dir)
	mustRegister(t, r, project, left)
	if err := r.Warm(project, left, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, registryv1.Registry{Files: []registryv1.Entry{{Path: kept, Offset: 9, NextSegment: 1}}}); err != nil {
		t.Fatal(err)
	}

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	if paths, err := registeredPaths(r); err != nil || !slices.Equal(paths, []string{kept}) {
		t.Errorf("Contents().Paths = %q, %v, want only the entry of the project file", paths, err)
	}
	if _, err := os.Stat(sessionFileOf(dir, left, ".heat")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("heat left in the directory: stat = %v, want it gone", err)
	}
}

func TestMigrate_RewritesFromItsCopyOnlyTheEntriesAnEarlierTryLeftUnreadable(t *testing.T) {
	dir := t.TempDir()
	empty, partial, whole := abs(t, "empty.jsonl"), abs(t, "partial.jsonl"), abs(t, "whole.jsonl")
	if err := registryv1.LeaveHalfway(dir, project, registryv1.Registry{Files: []registryv1.Entry{
		{Path: empty, Offset: 11, NextSegment: 2, MessageIDs: []string{"e1"}},
		{Path: partial, Offset: 22, NextSegment: 3, MessageIDs: []string{"p1"}},
		{Path: whole, Offset: 33, NextSegment: 4, MessageIDs: []string{"w1"}},
	}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, entryFileOf(dir, empty), "")
	writeFile(t, entryFileOf(dir, partial), `{"path":`+strconv.Quote(partial)+`,"offset":2`)
	standing := `{"path":` + strconv.Quote(whole) + `,"inode":0,"size":0,"offset":99,"next_segment":9,"message_ids":["w9"]}`
	writeFile(t, entryFileOf(dir, whole), standing)
	r := follow.Open(dir)

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatalf("Files() = %v", err)
	}
	got := map[string]follow.File{}
	for _, f := range files {
		got[f.Path] = f
	}
	for _, want := range []follow.File{
		{Path: empty, Offset: 11, NextSegment: 2, MessageIDs: []string{"e1"}},
		{Path: partial, Offset: 22, NextSegment: 3, MessageIDs: []string{"p1"}},
	} {
		if f := got[want.Path]; f.Offset != want.Offset || f.NextSegment != want.NextSegment || !slices.Equal(f.MessageIDs, want.MessageIDs) {
			t.Errorf("entry of %s = %+v, want the cursor of the copy: %+v", filepath.Base(want.Path), f, want)
		}
	}
	if data, err := os.ReadFile(entryFileOf(dir, whole)); err != nil || string(data) != standing {
		t.Errorf("entry of whole.jsonl = %q, %v, want the one that stood left as it was", data, err)
	}
	if n, err := unreadable(r, project); err != nil || n != 0 {
		t.Errorf("Contents().Unreadable = %d, %v, want 0", n, err)
	}
}

func TestRegisterAll_EmptiesAProjectDirectoryLeftWithoutAProjectFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	left, registered := abs(t, "left.jsonl"), abs(t, "registered.jsonl")
	r := follow.Open(dir)
	mustRegister(t, r, project, left)
	if err := os.Remove(projectFileOf(dir)); err != nil {
		t.Fatal(err)
	}

	mustRegister(t, r, project, registered)

	if paths, err := registeredPaths(r); err != nil || !slices.Equal(paths, []string{registered}) {
		t.Errorf("Contents().Paths = %q, %v, want only the path registered after the project file was gone", paths, err)
	}
}

func TestMigrate_LeavesAnEarlierProjectFileThatDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	const broken = `{"version":1,"files":[{"path":`
	if err := os.WriteFile(projectFileOf(dir), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)

	err := r.Migrate(project, time.Now(), follow.ProcessAlive)

	if err == nil || errors.Is(err, follow.ErrMigrationPending) {
		t.Errorf("Migrate() = %v, want the parse error", err)
	}
	if _, err := r.Files(project); err == nil {
		t.Error("Files() = nil error, want the registry refused")
	}
	if got, err := os.ReadFile(projectFileOf(dir)); err != nil || string(got) != broken {
		t.Errorf("project file = %q, %v, want it left as it was", got, err)
	}
	if _, err := os.Stat(projectDirOf(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("project directory: stat = %v, want none made", err)
	}
}

func TestMigrate_LeavesAProjectInThisLayoutAsItIs(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	main := abs(t, "s.jsonl")
	mustRegister(t, r, project, main)
	before := tree(t, dir)

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}
	if err := r.Migrate("ffff0000", time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() of a project with no registry = %v", err)
	}

	if diff := treeDiff(tree(t, dir), before); diff != "" {
		t.Errorf("Migrate() changed a registry in this layout:\n%s", diff)
	}
}

func TestMigrate_LeavesBehindTheColdEntriesWhoseFileIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	sessions := t.TempDir()
	at := func(name string) string { return filepath.Join(sessions, name) }
	kept, keptAgent := at("kept.jsonl"), agentOf(at("kept.jsonl"), "agent-k.jsonl")
	gone := at("gone.jsonl")
	mainGone, agentKept := at("main-gone.jsonl"), agentOf(at("main-gone.jsonl"), "agent-m.jsonl")
	unmounted := filepath.Join(sessions, "unmounted", "u.jsonl")
	hot := at("hot.jsonl")
	later := at("later.jsonl")
	for _, path := range []string{kept, keptAgent, agentKept} {
		writeFile(t, path, userLine(1))
	}
	const cold = "2026-11-01T09:00:00Z"
	recent := time.Now().UTC().Format(time.RFC3339)
	signals := drift.Signals{AssistantLines: 4}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	err := registryv1.Write(dir, project, registryv1.Registry{
		Files: []registryv1.Entry{
			{Path: kept, Offset: 1, NextSegment: 1, LastEvent: cold, Told: true},
			{Path: keptAgent, Offset: 2, NextSegment: 1},
			{Path: gone, Offset: 3, NextSegment: 1, LastEvent: cold, Told: true},
			{Path: mainGone, Offset: 4, NextSegment: 1, LastEvent: cold, Told: true},
			{Path: agentKept, Offset: 5, NextSegment: 1},
			{Path: unmounted, Offset: 6, NextSegment: 1},
			{Path: hot, Offset: 7, NextSegment: 1, LastEvent: recent},
			{Path: later, Offset: 8, NextSegment: 1, Retired: "kept-for-later"},
		},
		Signals: signals,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	want := slices.Sorted(slices.Values([]string{kept, keptAgent, agentKept, unmounted, hot, later}))
	if paths, err := registeredPaths(r); err != nil || !slices.Equal(paths, want) {
		t.Errorf("Contents().Paths = %q, %v\nwant %q: every entry but the cold ones whose file is gone from a directory still there", paths, err, want)
	}
	for _, mark := range []string{".heat", ".told"} {
		if _, err := os.Stat(sessionFileOf(dir, gone, mark)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s of a session with no entry carried: stat = %v, want none", mark, err)
		}
		if _, err := os.Stat(sessionFileOf(dir, mainGone, mark)); err != nil {
			t.Errorf("%s of a session whose agent file is carried: stat = %v, want it there", mark, err)
		}
	}
	if got, err := signalsOf(r); err != nil || got.AssistantLines != signals.AssistantLines {
		t.Errorf("Contents().Signals = %+v, %v, want the project's %+v", got, err, signals)
	}
}

func TestMigrate_KeepsEveryEntryOfALargeRegistryWhoseFileIsThere(t *testing.T) {
	n := largeRegistryEntries()
	dir := filepath.Join(t.TempDir(), "follow")
	reg, kept, gone := largeRegistryWithFilesGone(t, n)
	removeAll(t, gone)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, reg); err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	if paths, err := registeredPaths(r); err != nil || !slices.Equal(paths, kept) {
		t.Errorf("Contents().Paths lists %d entries, %v, want the %d of %d whose file is there", len(paths), err, len(kept), n)
	}
}

// largeRegistryWithFilesGone is an earlier registry of n cold entries,
// twenty to a session, whose files lie in one directory, and the paths,
// sorted, of the entries whose files are still there and of those whose
// files are gone: every fifth file, and every file of every seventh
// session. The files that are gone are on disk until removeAll removes
// them.
func largeRegistryWithFilesGone(t *testing.T, n int) (reg registryv1.Registry, kept, gone []string) {
	t.Helper()
	sessions := t.TempDir()
	for i := range n {
		session := filepath.Join(sessions, fmt.Sprintf("s%03d.jsonl", i/20))
		path := session
		if i%20 != 0 {
			path = agentOf(session, fmt.Sprintf("agent-%02d.jsonl", i%20))
		}
		writeFile(t, path, userLine(1))
		reg.Files = append(reg.Files, registryv1.Entry{
			Path: path, Size: 10, Offset: 10, NextSegment: 1, MessageIDs: []string{fmt.Sprintf("msg_%d", i)},
		})
		if i%5 == 0 || (i/20)%7 == 0 {
			gone = append(gone, path)
		} else {
			kept = append(kept, path)
		}
	}
	slices.Sort(kept)
	return reg, kept, gone
}

// removeAll removes every file of paths.
func removeAll(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrate_LeavesBehindTheAgentEntriesOfASessionRemovedWithItsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	removed, unmounted := sessionsWithAgentFiles(t)
	var reg registryv1.Registry
	for _, path := range slices.Concat(removed, unmounted) {
		reg.Files = append(reg.Files, registryv1.Entry{Path: path, Offset: 1, NextSegment: 1})
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, reg); err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	if paths, err := registeredPaths(r); err != nil || !slices.Equal(paths, unmounted) {
		t.Errorf("Contents().Paths = %q, %v\nwant the entries of the session whose main file's directory is not there: %q", paths, err, unmounted)
	}
}

func TestMigrate_TakesAgainTheEntryLocksThatGrewOldBeforeItRewritesTheProjectFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, registryv1.Registry{Files: []registryv1.Entry{{Path: path}}}); err != nil {
		t.Fatal(err)
	}
	locked := 0
	follow.AfterMigrationSteps(t, func(step int) {
		if step != 1 {
			return
		}
		locked++
		if locked > 1 {
			return
		}
		locks, err := filepath.Glob(filepath.Join(dir, project+".*.lock"))
		if err != nil || len(locks) != 1 {
			t.Errorf("entry locks held after step 1 = %q, %v, want the one of the entry", locks, err)
			return
		}
		old := time.Now().Add(-90 * time.Second)
		if err := os.Chtimes(locks[0], old, old); err != nil {
			t.Error(err)
		}
	})

	if err := follow.Open(dir).Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	if locked != 2 {
		t.Errorf("the entry locks were taken %d times, want twice: an earlier reader takes over a lock as old as the first ones were", locked)
	}
	if paths, err := registeredPaths(follow.Open(dir)); err != nil || !slices.Equal(paths, []string{path}) {
		t.Errorf("Contents().Paths = %q, %v, want the entry migrated", paths, err)
	}
}

func TestMigrate_MakesNoEntryOnceItsCopyIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	sessions := t.TempDir()
	first, second, third := filepath.Join(sessions, "sid-1.jsonl"), filepath.Join(sessions, "sid-2.jsonl"), filepath.Join(sessions, "sid-3.jsonl")
	for _, path := range []string{first, second, third} {
		writeFile(t, path, userLine(1))
	}
	recent := time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, registryv1.Registry{Files: []registryv1.Entry{
		{Path: first, Offset: 1, NextSegment: 1},
		{Path: second, Offset: 2, NextSegment: 1, LastEvent: recent, Told: true},
		{Path: third, Offset: 3, NextSegment: 1, LastEvent: recent, Told: true},
	}}); err != nil {
		t.Fatal(err)
	}
	var left map[string]string
	follow.BeforeCarries(t, func(path string) {
		if path != second || left != nil {
			return
		}
		if err := os.Remove(filepath.Join(projectDirOf(dir), "migrating.v1")); err != nil {
			t.Error(err)
		}
		left = tree(t, projectDirOf(dir))
	})

	if err := follow.Open(dir).Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v, want a copy another process removed taken as a migration it finished", err)
	}

	if left == nil {
		t.Fatal("the migration never came to the second entry")
	}
	after := tree(t, projectDirOf(dir))
	maps.DeleteFunc(after, func(name, _ string) bool { return strings.HasSuffix(name, ".lock") })
	maps.DeleteFunc(left, func(name, _ string) bool { return strings.HasSuffix(name, ".lock") })
	if diff := treeDiff(after, left); diff != "" {
		t.Errorf("project directory after the copy was removed in the middle of step 5 changed:\n%s", diff)
	}
}

// pendingFileOf is the pending registration of path.
func pendingFileOf(dir, path string) string {
	return filepath.Join(projectDirOf(dir), digestOf(path)+".pending")
}

func TestMigrate_MakesAnEntryOfAFileRegisteredWhileTheRegistryWaitedAndKeepsAnEarlierCursor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	sessions := t.TempDir()
	earlier, named := filepath.Join(sessions, "sid-1.jsonl"), filepath.Join(sessions, "sid-2.jsonl")
	for _, path := range []string{earlier, named} {
		writeFile(t, path, userLine(1))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, registryv1.Registry{Files: []registryv1.Entry{
		{Path: earlier, Offset: 9, NextSegment: 2, MessageIDs: []string{"m1"}},
	}}); err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)

	if err := r.RegisterAll(project, []string{earlier, named}, "sub"); !errors.Is(err, follow.ErrMigrationPending) {
		t.Fatalf("RegisterAll() while the registry waits = %v, want ErrMigrationPending", err)
	}
	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	want := []follow.File{
		{Path: earlier, Offset: 9, NextSegment: 2, MessageIDs: []string{"m1"}},
		{Path: named, Subpath: "sub"},
	}
	if len(files) != len(want) {
		t.Fatalf("Files() = %+v, want %+v", files, want)
	}
	for i, f := range files {
		if !sameFile(f, want[i]) || f.Subpath != want[i].Subpath {
			t.Errorf("entry of %s = %+v, want %+v", filepath.Base(f.Path), f, want[i])
		}
	}
	for _, path := range []string{earlier, named} {
		if _, err := os.Stat(pendingFileOf(dir, path)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("pending registration of %s after the migration: stat = %v, want it taken", filepath.Base(path), err)
		}
	}
}

func TestMigrate_MakesAnEntryOfAPendingRegistrationPlacedAfterItsCopyIsRemoved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "follow")
	sessions := t.TempDir()
	earlier, late := filepath.Join(sessions, "sid-1.jsonl"), filepath.Join(sessions, "sid-2.jsonl")
	for _, path := range []string{earlier, late} {
		writeFile(t, path, userLine(1))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, registryv1.Registry{Files: []registryv1.Entry{{Path: earlier, Offset: 9, NextSegment: 2}}}); err != nil {
		t.Fatal(err)
	}
	follow.AfterMigrationSteps(t, func(step int) {
		if step != 6 {
			return
		}
		if _, err := os.Stat(filepath.Join(projectDirOf(dir), "migrating.v1")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("copy after step 6: stat = %v, want it removed", err)
		}
		writeFile(t, pendingFileOf(dir, late), `{"path":`+strconv.Quote(late)+`,"subpath":"sub"}`)
	})
	r := follow.Open(dir)

	if err := r.Migrate(project, time.Now(), follow.ProcessAlive); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}

	f, ok, err := r.File(project, late)
	if err != nil || !ok || f.Offset != 0 || f.Subpath != "sub" {
		t.Errorf("File() of the path a hook named as the copy was removed = %+v, %v, %v, want it registered from byte 0", f, ok, err)
	}
	if _, err := os.Stat(pendingFileOf(dir, late)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("pending registration after the migration: stat = %v, want it taken", err)
	}
}

func TestRegistry_RefusesToRecordInTheProjectFileOfAProjectHalfwayMigrated(t *testing.T) {
	dir := t.TempDir()
	if err := registryv1.LeaveHalfway(dir, project, registryv1.Registry{Files: []registryv1.Entry{{Path: abs(t, "s.jsonl")}}}); err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)
	before := tree(t, dir)

	gapsErr := r.SetGaps(project, follow.Gaps{Truncated: true})
	first, toldErr := r.TellReasoning(project, time.Now())

	if !errors.Is(gapsErr, follow.ErrMigrationPending) {
		t.Errorf("SetGaps() = %v, want ErrMigrationPending", gapsErr)
	}
	if first || !errors.Is(toldErr, follow.ErrMigrationPending) {
		t.Errorf("TellReasoning() = %v, %v, want false and ErrMigrationPending", first, toldErr)
	}
	if diff := treeDiff(tree(t, dir), before); diff != "" {
		t.Errorf("the refused writes changed the registry:\n%s", diff)
	}
}
