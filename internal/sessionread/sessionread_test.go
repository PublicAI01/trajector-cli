package sessionread

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// reading is one device's stores and one enabled project with one
// registered session file, which holds one assistant line without a
// message id.
type reading struct {
	t       *testing.T
	reader  Reader
	project Project
	path    string
	// entry is the entry file of path, spelled as the layout spells it.
	entry string
}

func newReading(t *testing.T) *reading {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "work")
	project := Project{Token: "tok-test-fake", Hash: "0123abcd", Root: root, Shape: routing.WithoutProxy}
	routes := routing.OpenStore(filepath.Join(dir, "routing.json"))
	if err := routes.Grant(routing.Grant{
		Token:         project.Token,
		ProjectIDHash: project.Hash,
		RootPath:      root,
		Upstream:      "https://api.anthropic.com",
		GrantedAt:     time.Now().UTC().Format(time.RFC3339),
		Shape:         project.Shape,
	}); err != nil {
		t.Fatal(err)
	}
	sp, err := spool.Open(filepath.Join(dir, "spool"), 0)
	if err != nil {
		t.Fatal(err)
	}
	followDir := filepath.Join(dir, "follow")
	r := &reading{
		t: t,
		reader: Reader{
			Registry:  follow.Open(followDir),
			Routes:    routes,
			Spool:     sp,
			Version:   "0.0.0-test",
			ReaderLog: filepath.Join(dir, "reader.log"),
			Home:      dir,
			Now:       time.Now,
		},
		project: project,
		path:    filepath.Join(dir, "sessions", "0f1e2d3c.jsonl"),
	}
	sum := sha256.Sum256([]byte(r.path))
	r.entry = filepath.Join(followDir, project.Hash, hex.EncodeToString(sum[:16])+".json")
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.path, []byte(assistantWithoutID(root)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.reader.Registry.RegisterAll(project.Hash, []string{r.path}, ""); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *reading) registered() follow.File {
	r.t.Helper()
	f, ok, err := r.reader.Registry.File(r.project.Hash, r.path)
	if err != nil || !ok {
		r.t.Fatalf("File() = %v, %v, want the registered entry", ok, err)
	}
	return f
}

func (r *reading) signals() drift.Signals {
	r.t.Helper()
	s, err := r.reader.Registry.Signals(r.project.Hash)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func assistantWithoutID(root string) string {
	return fmt.Sprintf(`{"type":"assistant","cwd":%q,"apiBlockIndex":0,"message":{"role":"assistant","content":[]}}`+"\n", root)
}

func TestRead_KeepsWhatASegmentNoticedInTheWriteThatMovesItsCursor(t *testing.T) {
	r := newReading(t)
	writes := registryWrites(t, r.entry)
	line := assistantWithoutID(r.project.Root)

	r.reader.Read(r.project, []string{r.path})

	if n := writes(); n != 2 {
		t.Errorf("entry written %d times for one segment that noticed something, want twice: its bound, then its cursor with what it noticed", n)
	}
	if f := r.registered(); f.Offset != int64(len(line)) || f.NextSegment != 1 {
		t.Errorf("cursor = %+v, want it past the segment", f)
	}
	if s := r.signals(); s.AssistantLines != 1 || s.AssistantLinesWithoutMessageID != 1 {
		t.Errorf("signals = %+v, want the assistant line without an id counted", s)
	}
}

// TestStore_KeepsWhatAReadThatPausesNoticedAndLeavesTheCursor hands the
// store a segment that ends in part of a line, which no reader
// produces from a file: it is what a reader that contradicted itself
// would hand over.
func TestStore_KeepsWhatAReadThatPausesNoticedAndLeavesTheCursor(t *testing.T) {
	r := newReading(t)
	writes := registryWrites(t, r.entry)
	cut := follow.Reader{
		Registry:      r.reader.Registry,
		ProjectIDHash: r.project.Hash,
		Options:       follow.ReadOptions{Root: r.project.Root},
		Capture:       envelope.Capture{ClientVersion: r.reader.Version, ProjectIDHash: r.project.Hash, Injection: InjectionValue(r.project.Shape)},
		Store: func(res follow.ReadResult) (follow.Storing, drift.Signals) {
			for i := range res.Segments {
				res.Segments[i].Lines = strings.TrimSuffix(res.Segments[i].Lines, "\n")
			}
			return r.reader.store(r.project, res)
		},
	}

	if cut.Advance(r.path) {
		t.Error("Advance = true, want the project's reading stopped")
	}

	if got, _ := r.reader.Routes.PausedReason(); got != routing.PauseRedactionDrift {
		t.Errorf("PausedReason = %q, want recording paused", got)
	}
	if n := writes(); n != 2 {
		t.Errorf("entry written %d times for a read that paused, want twice: its bound, then what it noticed", n)
	}
	if f := r.registered(); f.Offset != 0 || f.NextSegment != 0 || f.ReadAt != "" {
		t.Errorf("cursor = %+v, want it left where it was", f)
	}
	if s := r.signals(); s.IncompleteSegments != 1 {
		t.Errorf("signals = %+v, want the read that ended in part of a line counted", s)
	}
}
