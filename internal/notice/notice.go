// Package notice keeps the lines this device has to tell its user the
// next time they run a command: that trajector upgraded itself in the
// background, and what that upgrade still asks of them. A line is said
// once. The command that shows it takes it, and taking it removes it.
//
// It is not the service notice. That one is a sentence the trajector
// service keeps in place until the service withdraws it, which status
// and doctor print every time they run. A line here is written by this
// device, for one command, and is gone once read.
//
// Each line is a rendered English sentence, stored as it will be shown.
// There is no schema to keep across builds: a newer build writes the
// sentence it means, and an older build that takes it shows it as it is.
//
// Two bounds keep a file nobody reads from growing: a line already
// waiting is not added a second time, and at most MaxLines wait, the
// newest kept. A user who has not run a command for weeks needs the
// latest few things this device has to say, not a log of them.
package notice

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// MaxLines is how many lines wait at most. Each upgrade writes a few
// lines, so twenty holds what several upgrades left behind.
const MaxLines = 20

// fileMode keeps the file private to the user, like everything else
// trajector stores.
const fileMode = 0o600

// Store is the file of lines waiting to be shown.
type Store struct {
	path string
}

// Open returns the store kept at path. Nothing is read or created until
// a line is added or taken.
func Open(path string) *Store { return &Store{path: path} }

// Add puts lines in front of the next command the user runs. An empty
// line, and one that already waits, is left out; when more than
// MaxLines would wait, the oldest go. A file that does not parse is
// replaced by the lines added: what it held can never be shown.
func (s *Store) Add(lines ...string) error {
	if err := userdirs.EnsureOwnerDir(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("notice: %w", err)
	}
	err := fsatomic.Update(s.path, fileMode, func(old []byte) ([]byte, error) {
		waiting := parse(old)
		before := len(waiting)
		for _, line := range lines {
			if line != "" && !slices.Contains(waiting, line) {
				waiting = append(waiting, line)
			}
		}
		if len(waiting) == before {
			return nil, fsatomic.ErrUnchanged
		}
		waiting = waiting[max(0, len(waiting)-MaxLines):]
		return json.Marshal(waiting)
	})
	if err != nil {
		return fmt.Errorf("notice: %w", err)
	}
	return nil
}

// Take returns every waiting line, oldest first, and removes them, so
// the next command shows nothing unless something new was added.
// A file that does not parse yields nothing.
func (s *Store) Take() ([]string, error) {
	// Most commands find nothing waiting. Looking first keeps them from
	// taking the file's lock for nothing; a line added after this look
	// waits for the next command.
	data, err := fsatomic.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("notice: %w", err)
	}
	if len(parse(data)) == 0 {
		return nil, nil
	}
	var taken []string
	err = fsatomic.Update(s.path, fileMode, func(old []byte) ([]byte, error) {
		taken = parse(old)
		return []byte("[]"), nil
	})
	if err != nil {
		return nil, fmt.Errorf("notice: %w", err)
	}
	return taken, nil
}

// parse reads the stored lines. Content that is not a list of strings
// is no lines at all.
func parse(data []byte) []string {
	var lines []string
	if json.Unmarshal(data, &lines) != nil {
		return nil
	}
	return slices.DeleteFunc(lines, func(line string) bool { return line == "" })
}
