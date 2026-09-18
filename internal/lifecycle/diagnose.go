package lifecycle

import (
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

// SessionFileReading says how much a diagnosis pays to learn about the
// current project's session files.
type SessionFileReading int

const (
	// FromRegistry reads the registry alone: the files it holds, and
	// what it recorded about the search that ran when the project was
	// enabled. Every surface can afford this reading.
	FromRegistry SessionFileReading = iota
	// FromTree adds a second, more expensive reading: a walk of the
	// project's directory tree as it stands now. It finds the session
	// files no hook of trajector's ever reported, and what it could not
	// cover supersedes the registry's older record of the same. Only a
	// caller that acts on the difference asks for it.
	FromTree
)

// Diagnose resolves the device's full state, the one value status,
// doctor, and the bundle each render. Stores that fail to open or read
// surface inside the value where a surface can present them; only the
// project resolution itself can fail the call. The reading decides how
// the session files are learned about, and the value says which one it
// carries, so a surface never has to ask again.
func (m *Machine) Diagnose(dir string, reading SessionFileReading) (report.Diagnosis, error) {
	d := report.Diagnosis{Version: m.deps.Version}
	st, err := m.Project(dir)
	if err != nil {
		return d, err
	}
	d.Project = st
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
	root, err := consent.CanonicalRoot(dir)
	if err != nil {
		return report.ProjectStatus{}, err
	}
	st := report.ProjectStatus{Root: root, Hash: consent.ProjectIDHash(root)}

	grant, enabled, err := m.routes.Active(root)
	if err != nil {
		return st, err
	}
	if enabled {
		st.Enabled = true
		st.Token = grant.Token
		st.Upstream = grant.Upstream
		st.UpstreamMoved = grant.UpstreamMoved
		st.GrantHash = grant.ProjectIDHash
		st.Shape = grant.Shape
	}
	if st.PauseReason, err = m.routes.PausedReason(); err != nil {
		return st, err
	}

	settings := st.SettingsPath()
	if url, ok := claudesettings.InjectedBaseURL(settings); ok {
		st.InjectedBaseURL = url
		st.InjectedToken, _ = claudesettings.TokenFromBaseURL(url)
	}
	st.HookInstalled = claudesettings.HasHook(settings, claudesettings.EnsureProxyMarker)
	st.SessionEndInstalled = claudesettings.HasHook(settings, claudesettings.SessionEndMarker)
	st.GitSnapshotInstalled = claudesettings.HasHook(settings, claudesettings.GitSnapshotMarker)
	onFile, injected := claudesettings.InjectionShape(settings)
	st.Injected = injected
	st.InjectionAgrees = injectionAgrees(st, onFile)

	st.WindowsSideClaude = claudesettings.WindowsSideClaude(root, "")

	if st.AgreementVersion, _, err = m.consent.AcceptedVersion(); err != nil {
		return st, err
	}
	state, ok, err := m.consent.ProjectState(st.Hash)
	if err != nil {
		return st, err
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
	if !st.Enabled || !st.HookInstalled || onFile != st.Shape {
		return false
	}
	return st.Shape == routing.WithoutProxy || st.InjectedToken == st.Token
}

// sessionFilesState turns the project's registry into counts and
// sizes: it stats the registered files to measure what is not read yet
// and opens none of them. The registry is opened once here, whichever
// reading was asked for, so one run can never hold two accounts of it.
func (m *Machine) sessionFilesState(st report.ProjectStatus, reading SessionFileReading) report.SessionFilesState {
	registered := m.sessionFiles(st.Hash)
	state := report.SessionFilesState{Err: registered.Err, Gaps: registered.Gaps, Signals: registered.Signals}
	for _, f := range registered.Files {
		if f.MainSession() {
			state.Sessions++
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
	if reading == FromRegistry || state.Err != nil || st.WindowsSideClaude {
		return state
	}
	return m.readProjectTree(state, st, registered.Files)
}

// readProjectTree takes the second reading: it walks the project's tree
// once and holds what it found against the files the registry holds.
// It registers nothing — registering is enable's and the session hooks'
// alone, and a run that diagnoses must leave the state it diagnosed as
// it found it.
func (m *Machine) readProjectTree(state report.SessionFilesState, st report.ProjectStatus, registered []follow.File) report.SessionFilesState {
	found, err := discover.Walk(st.Root, m.claude().ConfigDir)
	if err != nil {
		state.WalkErr = err
		return state
	}
	held := make(map[string]bool, len(registered))
	for _, f := range registered {
		held[f.Path] = true
	}
	state.Walked = true
	state.Gaps = found.Gaps
	for _, path := range found.Sessions {
		if !held[path] {
			state.Unregistered++
		}
	}
	return state
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
