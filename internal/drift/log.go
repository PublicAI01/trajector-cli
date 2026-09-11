package drift

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// LogEntry is one line of the log of what reading noticed: when, for
// which project, whether reading stopped there, and the signals
// themselves. It holds no path and no session id, because Signals
// holds none.
type LogEntry struct {
	At            string `json:"at"`
	ProjectIDHash string `json:"project_id_hash"`
	// Stop repeats what Signals.Stop reports, so a line states the
	// outcome it belongs to on its own.
	Stop bool `json:"stop,omitempty"`
	Signals
}

// AppendLog adds one entry to the log at path for a scan that found
// something, creating the file and its directory owner-only. The
// readers of different projects run at once and share the file: each
// entry goes down in one append-mode write, which is what keeps lines
// whole.
func AppendLog(path, at, projectIDHash string, s Signals) error {
	if err := userdirs.EnsureOwnerDir(filepath.Dir(path)); err != nil {
		return err
	}
	line, err := json.Marshal(LogEntry{At: at, ProjectIDHash: projectIDHash, Stop: s.Stop(), Signals: s})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadLog reads the log at path, oldest entry first. A log nothing has
// written yet holds no entries. A line that is not an entry is an
// error: this package writes the file, so anything else in it is not
// this log.
func ReadLog(path string) ([]LogEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []LogEntry
	for n, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("drift: log line %d: %w", n+1, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
