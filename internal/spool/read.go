package spool

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	return s.eachLocked(func(r Rawcall, _ string) error { return visit(r) })
}

// DeleteWhere removes every stored rawcall matching keep and rewrites
// each affected day index. It exists for consent withdrawal, where one
// project's unuploaded data must go without touching any other
// project's records.
func (s *Spool) DeleteWhere(match func(Rawcall) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	deleted := 0
	perDay := map[string]map[string]bool{}
	err := s.eachLocked(func(r Rawcall, path string) error {
		if !match(r) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		s.usage -= r.Size
		dayDir := filepath.Dir(path)
		if perDay[dayDir] == nil {
			perDay[dayDir] = map[string]bool{}
		}
		perDay[dayDir][r.RequestID] = true
		deleted++
		return nil
	})
	if err != nil {
		return deleted, err
	}
	for dayDir, removed := range perDay {
		if err := s.rewriteIndexLocked(dayDir, removed); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (s *Spool) eachLocked(visit func(r Rawcall, path string) error) error {
	days, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		dayDir := filepath.Join(s.dir, day.Name())
		indexed, err := readIndex(dayDir)
		if err != nil {
			return err
		}
		files, err := os.ReadDir(dayDir)
		if err != nil {
			return err
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || name == indexName || filepath.Ext(name) != ".json" {
				continue
			}
			path := filepath.Join(dayDir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			r := Rawcall{
				RequestID: strings.TrimSuffix(name, ".json"),
				Size:      int64(len(data)),
				Data:      data,
			}
			if line, ok := indexed[r.RequestID]; ok {
				r.SessionKey = line.SessionKey
				if ts, err := time.Parse(time.RFC3339Nano, line.Timestamp); err == nil {
					r.Timestamp = ts
				}
			}
			if r.Timestamp.IsZero() {
				if info, err := f.Info(); err == nil {
					r.Timestamp = info.ModTime().UTC()
				}
			}
			if err := visit(r, path); err != nil {
				return err
			}
		}
	}
	return nil
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
