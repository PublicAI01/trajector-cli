package spool

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// Rawcall is one stored record as a reader sees it.
type Rawcall struct {
	RequestID string
	// SessionKey and Timestamp come from the day index. A record the
	// index does not account for still reads back, with the timestamp
	// taken from the file itself and no session key.
	SessionKey string
	Timestamp  time.Time
	Size       int64
	Data       []byte
}

// rawcallFile is one day-directory entry holding a record. Building
// this list is the only place that knows which files in a day directory
// are rawcalls: the sidecar index is skipped, and so is anything that
// is not a .json file.
type rawcallFile struct {
	id   string
	path string
	size int64
	mod  time.Time
}

func rawcallFiles(dayDir string) ([]rawcallFile, error) {
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		return nil, err
	}
	var files []rawcallFile
	for _, f := range entries {
		name := f.Name()
		if f.IsDir() || name == indexName || filepath.Ext(name) != ".json" {
			continue
		}
		rf := rawcallFile{id: strings.TrimSuffix(name, ".json"), path: filepath.Join(dayDir, name)}
		if info, err := f.Info(); err == nil {
			rf.size = info.Size()
			rf.mod = info.ModTime().UTC()
		}
		files = append(files, rf)
	}
	return files, nil
}

// days lists the day directories, oldest first. A spool that was never
// written to has none.
func (s *Spool) days() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		if e.IsDir() {
			days = append(days, filepath.Join(s.dir, e.Name()))
		}
	}
	return days, nil
}

// Each visits every stored rawcall, oldest day first, stopping at the
// first error the visitor returns.
//
// The index leads and the day directory settles the result: an index
// entry naming a file that is no longer there is ignored, and a file the
// index never mentioned is visited anyway. Rawcall files are the source
// of truth, so a lost or corrupt index costs metadata, never records.
func (s *Spool) Each(visit func(Rawcall) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	days, err := s.days()
	if err != nil {
		return err
	}
	for _, dayDir := range days {
		indexed, err := readIndex(dayDir)
		if err != nil {
			return err
		}
		files, err := rawcallFiles(dayDir)
		if err != nil {
			return err
		}
		for _, f := range files {
			data, err := os.ReadFile(f.path)
			if err != nil {
				return err
			}
			r := Rawcall{RequestID: f.id, Size: int64(len(data)), Data: data}
			if line, ok := indexed[f.id]; ok {
				r.SessionKey = line.SessionKey
				if ts, err := time.Parse(time.RFC3339Nano, line.Timestamp); err == nil {
					r.Timestamp = ts
				}
			}
			if r.Timestamp.IsZero() {
				r.Timestamp = f.mod
			}
			if err := visit(r); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteWhere removes every stored rawcall whose request id matches and
// rewrites each affected day index. Matching on the id alone means no
// record body is ever read: the uploader deletes what it has shipped
// without paying to reread it.
func (s *Spool) DeleteWhere(match func(requestID string) bool) (int, error) {
	return s.deleteMatching(func(f rawcallFile, _ map[string]indexLine) (bool, error) {
		return match(f.id), nil
	})
}

// DeleteProject removes every stored rawcall belonging to one project.
// It exists for consent withdrawal, which must work even on records it
// cannot read: the index attributes each record, a record the index
// missed is attributed from its own bytes, and a record attributable to
// no project at all is kept rather than guessed at.
func (s *Spool) DeleteProject(projectIDHash string) (int, error) {
	return s.deleteMatching(func(f rawcallFile, indexed map[string]indexLine) (bool, error) {
		if line, ok := indexed[f.id]; ok && line.ProjectIDHash != "" {
			return line.ProjectIDHash == projectIDHash, nil
		}
		data, err := os.ReadFile(f.path)
		if err != nil {
			return false, err
		}
		hash, ok := envelope.ProjectIDHashOf(data)
		return ok && hash == projectIDHash, nil
	})
}

func (s *Spool) deleteMatching(match func(f rawcallFile, indexed map[string]indexLine) (bool, error)) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()

	deleted := 0
	days, err := s.days()
	if err != nil {
		return 0, err
	}
	for _, dayDir := range days {
		indexed, err := readIndex(dayDir)
		if err != nil {
			return deleted, err
		}
		files, err := rawcallFiles(dayDir)
		if err != nil {
			return deleted, err
		}
		removed := map[string]bool{}
		for _, f := range files {
			ok, err := match(f, indexed)
			if err != nil {
				return deleted, err
			}
			if !ok {
				continue
			}
			if err := os.Remove(f.path); err != nil {
				return deleted, err
			}
			s.usage -= f.size
			removed[f.id] = true
			deleted++
		}
		if len(removed) > 0 {
			if err := s.rewriteIndexLocked(dayDir, removed); err != nil {
				return deleted, err
			}
		}
	}
	s.sig = dirSignature(s.dir)
	return deleted, nil
}

// Oldest reports when the oldest stored rawcall was captured, so a
// caller can weigh the spool's age without reading any record data. It
// consults only the earliest day that holds records: its index leads
// and file modification times settle records the index missed.
func (s *Spool) Oldest() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	days, err := s.days()
	if err != nil {
		return time.Time{}, false
	}
	for _, dayDir := range days {
		indexed, err := readIndex(dayDir)
		if err != nil {
			return time.Time{}, false
		}
		files, err := rawcallFiles(dayDir)
		if err != nil {
			return time.Time{}, false
		}
		var oldest time.Time
		found := false
		for _, f := range files {
			var ts time.Time
			if line, ok := indexed[f.id]; ok {
				ts, _ = time.Parse(time.RFC3339Nano, line.Timestamp)
			}
			if ts.IsZero() {
				ts = f.mod
			}
			if !found || ts.Before(oldest) {
				oldest = ts
				found = true
			}
		}
		if found {
			return oldest, true
		}
	}
	return time.Time{}, false
}

// readIndex loads a day's sidecar index. A missing index is the
// rebuild-from-disk case, not a failure, and an unreadable line costs
// only the metadata it carried.
func readIndex(dayDir string) (map[string]indexLine, error) {
	data, err := os.ReadFile(filepath.Join(dayDir, indexName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	indexed := map[string]indexLine{}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line indexLine
		if json.Unmarshal(raw, &line) != nil {
			continue
		}
		indexed[line.RequestID] = line
	}
	return indexed, nil
}
