package drift

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Size of the log on disk. A reader that meets a log at or above
// logSizeLimit rewrites it to its newest entries before it appends, so
// a device that reads session files for months holds a log of a known
// size. A rewrite keeps logKeepBytes and not the whole limit, so the
// appends that follow one rewrite do not each ask for another.
const (
	logSizeLimit = 1 << 20
	logKeepBytes = logSizeLimit / 2
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
//
// A log that has reached its size limit is first rewritten to its
// newest entries. Two readers that meet a full log at the same moment
// are serialized by the rewrite, but a third reader's append may land
// between one rewrite's read and the file it installs, and is then
// gone: the accepted cost is a few lines of a log of what was
// noticed. No count is lost with them, because a store keeps every
// scan's counts on its own path.
func AppendLog(path, at, projectIDHash string, s Signals) error {
	if err := userdirs.EnsureOwnerDir(filepath.Dir(path)); err != nil {
		return err
	}
	line, err := json.Marshal(LogEntry{At: at, ProjectIDHash: projectIDHash, Stop: s.Stop(), Signals: s})
	if err != nil {
		return err
	}
	if err := trimLog(path); err != nil {
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

// trimLog rewrites the log at path to its newest entries once it has
// reached the size limit. It goes through the write technique for
// shared files, so a reader of the log observes the whole of the
// previous file or the whole of the shorter one.
func trimLog(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < logSizeLimit {
		return nil
	}
	return fsatomic.Update(path, 0o600, func(old []byte) ([]byte, error) {
		return logTail(old, logKeepBytes), nil
	})
}

// logTail is the newest part of the log that fits in max bytes, whole
// entries only: half an entry is not an entry, and ReadLog refuses a
// line this package did not write. The newest entry is kept even when
// it alone is above max, because what a rewrite drops is age.
func logTail(data []byte, max int) []byte {
	lines := bytes.SplitAfter(data, []byte("\n"))
	start, size := len(lines), 0
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) == 0 {
			continue
		}
		if size+len(lines[i]) > max && start < len(lines) {
			break
		}
		size += len(lines[i])
		start = i
	}
	return bytes.Join(lines[start:], nil)
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
