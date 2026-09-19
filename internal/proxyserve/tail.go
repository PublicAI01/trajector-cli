package proxyserve

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/sessionread"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// sweepInterval is how often a served proxy looks at the hot files on
// its own. Hooks are the main way a file gets read while a session
// runs; the sweep covers a session whose hooks are disabled, a proxy
// that started after the session did, and a session killed before its
// last hook ran. Cold files are never looked at: a hook naming one is
// what makes it hot again.
const sweepInterval = 5 * time.Minute

// coolGrace is how long a hot file with no running process behind it
// may stay unchanged before the sweep reads it to its end, flushes,
// and marks it cold. It is longer than one sweep so that a process the
// registry cannot see — a shell that exited with the hook — does not
// cool a session that is merely thinking.
const coolGrace = 10 * time.Minute

// maxProgressBody bounds one progress report. A report is one path
// and two numbers; anything past this is not one.
const maxProgressBody = 1 << 16

// tailer reads registered session files from inside the served proxy:
// one file at once when a hook reports it gained lines, and the hot
// set on a cadence. It is the resident half of session reading; the
// rule for what a read stores is sessionread's, shared with the
// one-shot reader a hook starts when no proxy is up.
type tailer struct {
	reader   sessionread.Reader
	routes   *routing.Store
	uploader *upload.Uploader
	alive    func(pid int) bool
	logf     func(string, ...any)
	// nudge hands the flush cadence a flush a hook's read made due.
	// Reading on a hook's word may not upload on the hook's clock, so
	// what the hook path asks for it asks here.
	nudge *flushNudge

	mu sync.Mutex
	// lastActivity is when a hot file last gained lines or was named
	// by a hook. attended reports whether, at the last look, a process
	// behind a hot file was still running. Both feed idle exit.
	lastActivity time.Time
	attended     bool
}

// liveness is what idle exit reads: the activity clock and whether a
// session process is still running.
func (t *tailer) liveness() apiproxy.Liveness {
	t.mu.Lock()
	defer t.mu.Unlock()
	return apiproxy.Liveness{LastActivity: t.lastActivity, Attended: t.attended}
}

func (t *tailer) touch(now time.Time) {
	t.mu.Lock()
	if now.After(t.lastActivity) {
		t.lastActivity = now
	}
	t.mu.Unlock()
}

func (t *tailer) setAttended(attended bool) {
	t.mu.Lock()
	t.attended = attended
	t.mu.Unlock()
}

// handler serves the progress endpoint. A report is accepted as soon
// as it parses and names a registered file of an enabled project, and
// the read it asks for runs before the answer: it is one file, read
// from this device's own disk, and the caller needs the verdict on
// whether the file was read at all. A flush is not that — it opens
// the network and may take minutes on a slow link — so the answer
// never waits for one. The caller is a hook on the critical path of a
// session, held to a budget of seconds and killed past it, so the
// only thing it may ever wait for here is that one local read.
func (t *tailer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev apiproxy.Progress
		if err := json.NewDecoder(io.LimitReader(r.Body, maxProgressBody)).Decode(&ev); err != nil || ev.ProjectIDHash == "" || ev.Path == "" {
			http.Error(w, "a progress report names a project and a path", http.StatusBadRequest)
			return
		}
		if !t.progress(ev) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

// progress acts on one report: the session the named file belongs to
// is read, its agent files included. The heat is not written here.
// The hook marked the session hot — or cold, when it ended — before
// it reported, so the entries already say what the hook knows, and a
// second writer of the same fields could only take them back in time.
// The flush the read may have made due is left to the flush cadence;
// a session's end asks it for every record at once, because that
// session has no later report coming. It reports whether the file was
// one this device reads.
func (t *tailer) progress(ev apiproxy.Progress) bool {
	project, ok := t.project(ev.ProjectIDHash)
	if !ok {
		return false
	}
	files, err := t.reader.Registry.Files(ev.ProjectIDHash)
	if err != nil {
		return false
	}
	var file *follow.File
	for i := range files {
		if files[i].Path == ev.Path {
			file = &files[i]
			break
		}
	}
	if file == nil {
		return false
	}
	now := t.reader.Now()
	if ev.End {
		_ = t.reader.Registry.Cool(ev.ProjectIDHash, ev.Path)
		file.LastEvent, file.PID = "", 0
	} else {
		_ = t.reader.Registry.Warm(ev.ProjectIDHash, ev.Path, ev.PID, now)
		if ev.PID > 0 {
			file.PID = ev.PID
		}
		file.LastEvent = now.UTC().Format(time.RFC3339)
	}
	t.touch(now)
	if file.Attended(t.alive) {
		t.setAttended(true)
	}
	t.reader.Read(project, []follow.File{*file})
	t.nudge.ask(ev.End)
	return true
}

// sweep looks at every hot file once: one that grew is read, one whose
// process is gone and that has not grown for coolGrace is read to its
// end, flushed, and cooled. Cold files are not looked at. What the
// sweep learned about running processes is what idle exit reads until
// the next sweep or the next report. It runs on a goroutine of its
// own, which no hook waits on, so its flush is taken here and not
// handed to the flush cadence.
//
// What a sweep reads leaves at once rather than on the thresholds for
// records read from session files: a file the sweep found grown is a
// file whose hook never reported it, so its lines already missed the
// read that would have sent them on time, and a threshold of their
// own would only hold them longer.
//
// onStart marks the sweep a proxy runs on its way in, where that does
// not hold: a proxy that has just bound the port must not open an
// upload before it can be drained — the takeover of its port waits on
// the exit flush, which waits on that upload's lock.
func (t *tailer) sweep(onStart bool) {
	now := t.reader.Now()
	projects, err := t.reader.Registry.Projects()
	if err != nil {
		return
	}
	attended := false
	cooled := false
	read := false
	for _, hash := range projects {
		project, ok := t.project(hash)
		if !ok {
			continue
		}
		files, err := t.reader.Registry.Files(hash)
		if err != nil {
			continue
		}
		hot, _ := follow.Split(files, now, t.alive)
		for _, f := range hot {
			st, err := follow.StatFile(f.Path)
			if err != nil {
				continue
			}
			grew := f.React(st) != follow.Continue || f.Behind(st) > 0
			switch {
			case f.Attended(t.alive):
				attended = true
				if grew {
					t.touch(now)
					t.reader.Read(project, []follow.File{f})
					read = true
				}
			case grew:
				t.touch(now)
				_ = t.reader.Registry.Warm(hash, f.Path, 0, now)
				t.reader.Read(project, []follow.File{f})
				read = true
			case t.quietFor(f, now) >= coolGrace:
				t.reader.Read(project, []follow.File{f})
				_ = t.reader.Registry.Cool(hash, f.Path)
				cooled = true
			}
		}
	}
	t.setAttended(attended)
	// A sweep that read nothing flushes nothing: the flush cadence
	// owns the thresholds, and nothing new is waiting on them.
	switch {
	case cooled, read && !onStart:
		t.flushRecords()
	case read:
		t.flushDue()
	}
}

// quietFor is how long f has gone without an event, or forever for a
// file no hook ever named.
func (t *tailer) quietFor(f follow.File, now time.Time) time.Duration {
	at, err := time.Parse(time.RFC3339, f.LastEvent)
	if err != nil {
		return math.MaxInt64
	}
	return now.Sub(at)
}

// project is the enabled project a registry belongs to, by hash. A
// project with no standing grant has nothing read for it.
func (t *tailer) project(hash string) (sessionread.Project, bool) {
	grants, err := t.routes.All()
	if err != nil {
		return sessionread.Project{}, false
	}
	for _, g := range grants {
		if g.ProjectIDHash == hash && !g.Revoked {
			return sessionread.ProjectOf(g), true
		}
	}
	return sessionread.Project{}, false
}

// flushRecords sends what the spool holds now that a session is over
// or has gone quiet: the records just read are the last of it.
func (t *tailer) flushRecords() {
	if _, err := t.uploader.FlushRecords(); err != nil {
		t.logf("flush: %v", err)
	}
}

// flushDue is one threshold check, the same one the flush cadence
// runs: a read that just landed enough to pass a threshold should not
// wait for the next tick.
func (t *tailer) flushDue() {
	if _, err := t.uploader.Flush(false); err != nil {
		t.logf("flush: %v", err)
	}
}
