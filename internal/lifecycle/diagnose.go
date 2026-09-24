package lifecycle

import (
	"errors"
	"os"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// sessionFileReading says how much a diagnosis pays to learn about the
// current project's session files.
type sessionFileReading int

const (
	// fromRegistry reads the registry alone: the files it holds, and
	// what it recorded about the search that ran when the project was
	// enabled. Every surface can afford this reading.
	fromRegistry sessionFileReading = iota
	// fromTree adds a second, more expensive reading: a walk of the
	// project's directory tree as it stands now. It finds the session
	// files no hook of trajector's ever reported, and what it could not
	// cover supersedes the registry's older record of the same. Only a
	// caller that acts on the difference asks for it.
	fromTree
)

// diagnose resolves the device's full state, the one value status,
// doctor, and the bundle each render. Stores that fail to open or read
// surface inside the value where a surface can present them; only the
// project resolution itself can fail the call. The reading decides how
// the session files are learned about, and the value says which one it
// carries, so a surface never has to ask again.
func (m *Machine) diagnose(dir string, reading sessionFileReading) (report.Diagnosis, error) {
	d := report.Diagnosis{Version: m.deps.Version}
	st, err := m.observeProject(dir)
	if err != nil {
		return d, err
	}
	d.Project = st
	if grants, err := m.routes.All(); err == nil {
		d.EnabledProjects = len(grants)
	}
	if st.Enabled {
		d.OptionalSettings = m.optionalSettingStatuses(st)
		// Read fresh every time: the configuration it reads is the
		// user's or their organization's and changes without notice,
		// and a cached reading would report a state that no longer is.
		policy := m.hookPolicy(st.Root)
		d.HookPolicy = &policy
		d.SessionFiles = m.sessionFilesState(st, reading)
	}
	d.ProxyIdleBetweenSessions = m.proxyIdleBetweenSessions()

	// Observe, never Settled: only callers that act on the verdict pay
	// to wait out a sibling's startup. A diagnosis reports the port as
	// it stands and must answer at once.
	d.Proxy = m.proxy.Observe()
	if proxylife.PortIsHeld(d.Proxy.Reason) {
		// Only a holder this device may not use is looked into, and
		// only for what the operating system says about it: a surface
		// that names the process the user can look at themselves,
		// never a judgement about what the program is. A holder that
		// answered the challenge without proof is not one of them, and
		// reading the port for it costs a scan of every process for a
		// process no surface may name.
		if holder, read := proxylife.HolderOf(d.Proxy.Addr); read {
			d.ProxyHolder = report.HolderProcess{PID: holder.PID, Name: holder.Name}
		}
	}
	if st.Enabled && d.Proxy.Holder == proxylife.HolderOurs {
		if reply, err := m.proxy.Selfcheck(st.Token); err == nil {
			d.Selfcheck = &reply
		}
	}

	sp, spoolErr := m.spool()
	var days []spool.DaySummary
	if spoolErr == nil {
		days, spoolErr = sp.Summary()
	}
	spoolDir := m.deps.Layout.SpoolDir()
	if spoolErr != nil {
		d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: spoolErr}
	} else {
		d.Spool = report.SpoolState{
			Dir:         spoolDir,
			Usage:       sp.Usage(),
			Quota:       sp.Quota(),
			WritableErr: sp.Writable(),
			Days:        days,
		}
		d.Spool.OldestRecord = oldestWaiting(sp)
		d.Spool.Held = heldRecords(sp)
	}

	d.Uploads = upload.LoadState(m.deps.Layout.UploadDir())
	d.RejectedDir = m.deps.Layout.RejectedDir()
	d.Rejected, d.RejectedErr = upload.ListRejected(d.RejectedDir)
	d.Handshake = m.handshake()
	d.Standings = upload.LoadStandings(m.deps.Layout.UploadDir(), m.deps.Version, m.deps.Now())
	// The quarantine-only standing is derived here and nowhere else:
	// this is the one place that knows both halves of it — that the
	// spool has nothing left to send, and that batches are waiting in
	// quarantine. The sentence it prints still belongs to the standing.
	if d.Spool.OpenErr == nil && d.Spool.Usage == 0 && len(d.Rejected) > 0 {
		d.Standings = append(d.Standings, upload.Standing{Reason: upload.QuarantineOnly})
	}

	if path, moved := m.claude().UnreadUserSettingsPath(); moved {
		d.StaleDiscoveryHook = claudesettings.HasHook(path, claudesettings.DiscoveryMarker)
	}

	_, paired, err := m.tokens.DeviceToken()
	d.TokenStore = report.TokenStoreState{Paired: paired, Err: err}
	return d, nil
}

// heldRecords counts what the spool keeps on this machine because
// this build could not mask it. It is the one reading of the held
// slot: a slot that cannot be read counts as empty, because it holds
// nothing a surface must act on and a reading failure there is not a
// diagnosis of its own.
func heldRecords(sp *spool.Spool) report.HeldRecords {
	held, err := sp.Held()
	if err != nil {
		return report.HeldRecordsOf(nil)
	}
	return report.HeldRecordsOf(held)
}

// oldestWaiting is when the oldest record still in the spool was
// captured, of whichever kind that is: status reports one wait.
func oldestWaiting(sp *spool.Spool) time.Time {
	oldest, _ := sp.OldestEntry()
	return oldest
}

// Project resolves one project's full consent status. A store that
// cannot be read surfaces as the error; the zero fields of a partial
// status are never presented as facts.
func (m *Machine) Project(dir string) (report.ProjectStatus, error) {
	st, err := m.observeProject(dir)
	if err == nil && st.TableUnreadable != nil {
		return st, st.TableUnreadable
	}
	return st, err
}

// observeProject is Project for a caller that reports what it finds
// rather than acts on it. A routing table that exists and cannot be
// read is carried in the status instead of failing the call: which
// projects are enabled is then unknown, and that is exactly what
// status, doctor and a session hook must be able to say. Project
// still fails on it, because a caller that acts on a grant must never
// act on the zero one.
func (m *Machine) observeProject(dir string) (report.ProjectStatus, error) {
	root, err := consent.CanonicalRoot(dir)
	if err != nil {
		return report.ProjectStatus{}, err
	}
	st := report.ProjectStatus{Root: root, Hash: consent.ProjectIDHash(root)}

	grant, enabled, err := m.routes.Active(root)
	if unreadable, ok := errors.AsType[*routing.UnreadableError](err); ok {
		st.TableUnreadable = unreadable
	} else if err != nil {
		return st, err
	}
	if enabled {
		st.Enabled = true
		st.Token = grant.Token
		st.Upstream = grant.Upstream
		st.UpstreamMoved = grant.UpstreamMoved
		st.GrantHash = grant.ProjectIDHash
		st.Shape = grant.Shape
		st.EarlierSkipped = grant.EarlierSkipped
		st.GrantedAt, _ = grant.GrantedAtTime()
	}
	if st.TableUnreadable == nil {
		if st.PauseReason, err = m.routes.PausedReason(); err != nil {
			return st, err
		}
	}

	settings := st.SettingsPath()
	if url, ok := claudesettings.InjectedBaseURL(settings); ok {
		st.InjectedBaseURL = url
		st.InjectedToken, _ = claudesettings.TokenFromBaseURL(url)
	}
	st.Hooks = claudesettings.InstalledProjectHooks(settings)
	onFile, injected := claudesettings.InjectionShape(settings)
	st.Injected = injected
	st.InjectionAgrees = injectionAgrees(st, onFile)

	st.WindowsSideClaude = claudesettings.WindowsSideClaude(root, "")

	// A record that cannot be read is reported, not returned as a
	// failure of the whole reading: the pause it causes is exactly what
	// status and doctor exist to show, and they can only show it if
	// they still get a status.
	if st.AgreementVersion, _, err = m.consent.AcceptedVersion(); err != nil {
		st.ConsentPath, st.ConsentErr = m.consent.Path(), err
		return st, nil
	}
	state, ok, err := m.consent.ProjectState(st.Hash)
	if err != nil {
		st.ConsentPath, st.ConsentErr = m.consent.Path(), err
		return st, nil
	}
	if ok {
		st.ConsentState = state
	}
	return st, nil
}

// injectionAgrees holds the injection read from the settings file
// against the grant: it is the injection the grant calls for when it
// stands in the grant's shape and, in the shape that carries a base
// URL, carries this project's own token. It is decided here because
// this is the one place that has read both.
func injectionAgrees(st report.ProjectStatus, onFile routing.Shape) bool {
	if !st.Enabled || !st.Hooks.Has(claudesettings.HookEnsureProxy) || onFile != st.Shape {
		return false
	}
	return st.Shape == routing.WithoutProxy || st.InjectedToken == st.Token
}

// sessionFilesState turns the project's registry into counts and
// sizes: it stats the registered files to measure what is not read yet
// and opens none of them. Every count here is of what the project
// still reads, so one run can never hold two accounts of that.
func (m *Machine) sessionFilesState(st report.ProjectStatus, reading sessionFileReading) report.SessionFilesState {
	registered := m.sessionFiles(st.Hash)
	state := report.SessionFilesState{Err: registered.Err, Gaps: registered.Gaps, Signals: registered.Signals}
	for _, f := range registered.Files {
		if f.MainSession() {
			state.Sessions++
			if f.Outside {
				state.Outside++
			}
		}
		if at, ok := f.LastRead(); ok && at.After(state.LastReadAt) {
			state.LastReadAt = at
		}
		if s, err := follow.StatFile(f.Path); err == nil {
			state.BytesBehind += f.Behind(s)
		}
	}
	// A registry that could not be read leaves the walk nothing to be
	// held against, and a project Claude Code opens from the Windows
	// side keeps its session files on that side, where this process
	// cannot reach; the diagnosis already says so.
	if reading == fromRegistry || state.Err != nil || st.WindowsSideClaude {
		return state
	}
	return m.readProjectTree(state, st)
}

// readProjectTree takes the second reading: it walks the project's tree
// once and holds what it found against every entry the registry keeps,
// the retired ones included. An entry whose reading stopped for good is
// registered, and a search that called its path unregistered would ask
// the user to register what is registered already. The counts above
// come from what is still read, which is a different question.
//
// It registers nothing — registering is enable's and the session hooks'
// alone, and a run that diagnoses must leave the state it diagnosed as
// it found it.
func (m *Machine) readProjectTree(state report.SessionFilesState, st report.ProjectStatus) report.SessionFilesState {
	entries, err := m.registry.Entries(st.Hash)
	if err != nil {
		state.Err = err
		return state
	}
	found, err := discover.Walk(st.Root, m.claude().ConfigDir)
	if err != nil {
		state.WalkErr = err
		return state
	}
	held := make(map[string]bool, len(entries))
	for _, f := range entries {
		held[f.Path] = true
	}
	state.Walked = true
	state.Gaps = found.Gaps
	for _, path := range found.Sessions {
		if held[path] {
			continue
		}
		state.Unregistered++
		if earlierThanGrant(path, st.GrantedAt) {
			state.Earlier++
		}
	}
	return state
}

// earlierThanGrant reports that the file at path was last written
// before the project was enabled, which is what separates a session
// no hook could have reported from one a hook should have. A file
// this process cannot stat, and a grant with no readable time, answer
// false: the sessions this device cannot date are the ones it must
// not explain away.
func earlierThanGrant(path string, grantedAt time.Time) bool {
	if grantedAt.IsZero() {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.ModTime().Before(grantedAt)
}

// proxyIdleBetweenSessions reports that every enabled project on this
// device records without the proxy. On such a device the resident
// process lives only while a session runs, which is the healthy state,
// not a fault: a surface must not read a proxy that is absent between
// sessions as something to repair. The shape is read from the grants,
// the same record a repair reasons from, so one doctor run cannot
// pass an idle proxy and then rewrite a project into needing it. It is
// false when no project is enabled, because then there is nothing the
// reading is about, and false when the routing table cannot be read:
// an unreadable table is its own finding, and a surface that read it
// as "nothing uses the proxy" would hide one.
func (m *Machine) proxyIdleBetweenSessions() bool {
	grants, err := m.routes.All()
	if err != nil {
		return false
	}
	enabled := 0
	for _, g := range grants {
		if g.Revoked {
			continue
		}
		enabled++
		if g.Shape != routing.WithoutProxy {
			return false
		}
	}
	return enabled > 0
}
