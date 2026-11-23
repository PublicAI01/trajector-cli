package follow

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

const (
	heatExt = ".heat"
	toldExt = ".told"
)

// heatRefresh is how old the heat a process wrote may grow before a
// hook from the same process writes it again. A session's hooks come
// many times a minute; heat read a minute late still reads as hot.
const heatRefresh = time.Minute

// heat is when a hook last named a session, and the process the
// session runs in, as that hook reported it.
type heat struct {
	LastEvent string `json:"last_event"`
	PID       int    `json:"pid"`
}

// sessionPath names the file of the session path belongs to that ext
// names.
func (r *Registry) sessionPath(projectIDHash, path, ext string) string {
	return filepath.Join(r.projectDir(projectIDHash), digest(SessionOf(path))+ext)
}

// readHeat is the heat in file. ok is false for heat that is not
// there or cannot be read: heat is disposable, and missing heat is a
// session no hook named.
func readHeat(file string) (h heat, ok bool) {
	data, err := fsatomic.ReadFile(file)
	if err != nil || json.Unmarshal(data, &h) != nil {
		return heat{}, false
	}
	return h, true
}

// warmed is f with the heat of its session. seen holds the heat of the
// sessions read so far, and may be nil.
func (r *Registry) warmed(projectIDHash string, f File, seen map[string]heat) File {
	file := r.sessionPath(projectIDHash, f.Path, heatExt)
	h, ok := seen[file]
	if !ok {
		h, _ = readHeat(file)
		if seen != nil {
			seen[file] = h
		}
	}
	f.LastEvent, f.PID = h.LastEvent, h.PID
	return f
}

// registered refuses a path that is not registered, as an update to it
// is.
func (r *Registry) registered(projectIDHash, path string) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if _, err := os.Lstat(r.entryPath(projectIDHash, path)); os.IsNotExist(err) {
		return ErrNotRegistered
	} else if err != nil {
		return err
	}
	return nil
}

// Warm records that a session hook named path at at, from the process
// pid, so the session path belongs to reads as hot from now on: heat
// belongs to the session, not to one of its files, and a hook names the
// main file only. A pid of 0 keeps the process the session's heat
// already carries: a hook that cannot tell which process runs the
// session must not erase one that could. Heat the same process wrote
// less than heatRefresh before at is left as it is. A path that is not
// registered is refused.
func (r *Registry) Warm(projectIDHash, path string, pid int, at time.Time) error {
	if err := r.registered(projectIDHash, path); err != nil {
		return err
	}
	file := r.sessionPath(projectIDHash, path, heatExt)
	was, ok := readHeat(file)
	if pid <= 0 {
		pid = was.PID
	}
	if last, err := time.Parse(readAtLayout, was.LastEvent); ok && err == nil && was.PID == pid && at.Sub(last) < heatRefresh {
		return nil
	}
	data, err := encode(heat{LastEvent: at.UTC().Format(readAtLayout), PID: pid})
	if err != nil {
		return err
	}
	return fsatomic.WriteFile(file, data, 0o600)
}

// Cool records that the session writing path is over: the session
// reads as cold until a hook names it again. A path that is not
// registered is refused.
func (r *Registry) Cool(projectIDHash, path string) error {
	if err := r.registered(projectIDHash, path); err != nil {
		return err
	}
	err := os.Remove(r.sessionPath(projectIDHash, path, heatExt))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Tell records that the session path belongs to has been told the
// device stopped recording, and reports whether this call is the one
// that recorded it. A session already told is answered false, so the
// caller says nothing a second time: a session is told once however
// many of its files exist. A path that is not registered is refused.
func (r *Registry) Tell(projectIDHash, path string) (first bool, err error) {
	if err := r.registered(projectIDHash, path); err != nil {
		return false, err
	}
	mark, err := os.OpenFile(r.sessionPath(projectIDHash, path, toldExt), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = mark.Close()
	return err == nil, err
}
