//go:build localcorpus

package drift_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
)

// TestLocalCorpus runs Scan over every session file on this device —
// under CLAUDE_CONFIG_DIR, or ~/.claude, then projects/ — read the way
// the reader reads it, so what is scanned is what a segment would
// hold and nothing the reader drops. It fails on any finding that
// would stop reading. It is the run that can notice the upstream
// format moved, which the frozen fixtures never can, and it is opted
// into with the localcorpus build tag so it runs only where a
// developer's own session files are. It prints counts and field names
// only: never a path, a session id, or a value.
func TestLocalCorpus(t *testing.T) {
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory")
		}
		root = filepath.Join(home, ".claude")
	}
	root = filepath.Join(root, "projects")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("no session files directory on this device")
	}

	var (
		files, unreadable, segments int
		total                       drift.Report
		stopFields                  = map[string]bool{}
		incomplete                  int
		newValues                   = map[string]map[string]bool{}
	)
	note := func(kind string, values []string) {
		for _, v := range values {
			if newValues[kind] == nil {
				newValues[kind] = map[string]bool{}
			}
			newValues[kind][v] = true
		}
	}
	// Every directory counts as consented to, so a session that moved
	// is read through rather than stopped at the move.
	opts := follow.ReadOptions{Authorized: func(string) bool { return true }}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		files++
		res, err := follow.Read(follow.File{Path: path}, envelope.TranscriptCapture{}, opts)
		if err != nil {
			unreadable++
			return nil
		}
		for _, seg := range res.Segments {
			segments++
			rep, err := drift.Scan([]byte(seg.Lines))
			if err != nil {
				unreadable++
				continue
			}
			if rep.IncompleteLine {
				incomplete++
			}
			for _, field := range rep.UnanchoredPathFields {
				stopFields[field] = true
			}
			total.AssistantLines += rep.AssistantLines
			total.AssistantLinesWithoutMessageID += rep.AssistantLinesWithoutMessageID
			total.MessagesWithBlockIndexGap += rep.MessagesWithBlockIndexGap
			total.AgentLines += rep.AgentLines
			total.AgentLinesWithoutParent += rep.AgentLinesWithoutParent
			note("launch surface", rep.NewLaunchSurfaces)
			note("attachment type", rep.NewAttachmentTypes)
			note("system subtype", rep.NewSystemSubtypes)
			note("top-level type", rep.NewTopLevelTypes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("files %d, segments %d, unreadable %d, segments ending without a newline %d", files, segments, unreadable, incomplete)
	t.Logf("assistant lines %d, without message id %d; messages with a block index gap %d",
		total.AssistantLines, total.AssistantLinesWithoutMessageID, total.MessagesWithBlockIndexGap)
	t.Logf("agent lines %d, without a parent field %d", total.AgentLines, total.AgentLinesWithoutParent)
	for kind, values := range newValues {
		names := make([]string, 0, len(values))
		for v := range values {
			names = append(names, v)
		}
		t.Logf("new %s values: %d (%s)", kind, len(names), strings.Join(names, ", "))
	}
	if len(stopFields) > 0 {
		names := make([]string, 0, len(stopFields))
		for f := range stopFields {
			names = append(names, f)
		}
		t.Errorf("fields holding an absolute path that the anchored list does not know: %s", strings.Join(names, ", "))
	}
}
