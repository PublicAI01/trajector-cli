package follow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

func TestWindowsStatSupportsLongPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("long-directory/", 24), "session.jsonl")
	writeFile(t, path, userLine(1))
	st, err := follow.StatFile(path)
	if err != nil || !st.Exists || st.FileID == "" {
		t.Fatalf("StatFile(long path) = %+v, %v", st, err)
	}
}

func TestWindowsFileIdentitySurvivesAppendAndDetectsReplacement(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	a.register(path, "")
	a.reader.Advance(path)
	first := a.entry(path)
	if first.FileID == "" || first.Offset == 0 {
		t.Fatalf("missing file identity or unread content: %+v", first)
	}
	appendFile(t, path, userLine(2))
	appended, err := follow.StatFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if appended.FileID != first.FileID || first.React(appended) != follow.Continue {
		t.Fatal("appending changed the file identity")
	}
	a.reader.Advance(path)
	before := a.entry(path)
	replaceFile(t, path, userLine(3)+userLine(4))
	replacement, err := follow.StatFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.FileID == before.FileID || replacement.Size != before.Size || before.React(replacement) != follow.Rewrite {
		t.Fatal("a same-sized replacement was not recognized")
	}
	a.reader.Advance(path)
	a.reader.Registry = follow.Open(a.dir)
	if after := a.entry(path); after.FileID != replacement.FileID || after.NextSegment != before.NextSegment+1 {
		t.Fatalf("replacement identity and cursor did not persist: %+v", after)
	}
	link := filepath.Join(filepath.Dir(path), "hardlink.jsonl")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	linked, err := follow.StatFile(link)
	if err != nil || linked.FileID != replacement.FileID {
		t.Fatalf("hard link identity differs: %+v, %v", linked, err)
	}
}
