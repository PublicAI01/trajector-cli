package report

import (
	"encoding/json"
	"net/url"
	"runtime"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// maskedToken masks itself when marshalled, so a token field added to
// the diagnosis rendering is masked by construction, not by someone
// remembering to call the masking function.
type maskedToken string

func (t maskedToken) MarshalJSON() ([]byte, error) {
	return json.Marshal(maskToken(string(t)))
}

// errString renders an error for the diagnosis, empty when nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The diagnosis wire shapes. Token fields are typed maskedToken;
// everything else in a Diagnosis is identities, counters, timestamps,
// and reasons.
type diagnosisWire struct {
	Project     projectWire    `json:"project"`
	Proxy       proxyWire      `json:"proxy"`
	Spool       spoolWire      `json:"spool"`
	Uploads     upload.State   `json:"uploads"`
	Rejected    []rejectedWire `json:"rejected"`
	RejectedErr string         `json:"rejected_err,omitempty"`
	Handshake   any            `json:"handshake"`
	// Standings is every reason uploads were held back when the bundle
	// was written, omitted when they were flowing. Support reads it to
	// tell a paused uploader apart from a broken one — both look like
	// "nothing is uploading" — and to tell which of the pauses it was
	// without reconstructing the judgement from a version number.
	Standings  []upload.Standing `json:"standings,omitempty"`
	TokenStore tokenStoreWire    `json:"token_store"`
	// Selfcheck is the live proxy's answer for the current project,
	// present only when a proxy of ours answered one.
	Selfcheck any `json:"selfcheck,omitempty"`
	// StaleDiscoveryHook says a discovery hook of ours stands in a
	// settings file Claude Code does not read on this device. It is a
	// fact about the device, not about the current project: the hook
	// belongs to the default configuration directory while the device
	// names another one.
	StaleDiscoveryHook bool `json:"stale_discovery_hook"`
}

type projectWire struct {
	Root             string      `json:"root"`
	ProjectIDHash    string      `json:"project_id_hash"`
	Enabled          bool        `json:"enabled"`
	Upstream         string      `json:"upstream"`
	Injected         bool        `json:"injected"`
	NoProxy          bool        `json:"no_proxy"`
	InjectedToken    maskedToken `json:"injected_token"`
	Token            maskedToken `json:"token"`
	HooksInstalled   bool        `json:"hooks_installed"`
	SessionEndOK     bool        `json:"session_end_installed"`
	GitSnapshotOK    bool        `json:"git_snapshot_installed"`
	AgreementVersion string      `json:"agreement_version"`
	ConsentState     string      `json:"consent_state"`
	PauseReason      string      `json:"pause_reason"`
	WindowsSide      bool        `json:"windows_side_claude"`
	// HookPolicy is present for an enabled project: the static
	// reading of whether its hooks load, and the setting that decided
	// against it.
	HookPolicy *hookPolicyWire `json:"hook_policy,omitempty"`
	// OptionalSettings is each optional Claude Code setting's state for
	// this project. It is keys and states: the settings are trajector's
	// own list, and no value the user set is read out of them.
	OptionalSettings []optionalSettingWire `json:"optional_settings,omitempty"`
	// SessionFiles is counts and sizes only. A session id or a session
	// file's path would name what the user worked on, and the bundle
	// is handed to someone else.
	SessionFiles sessionFilesWire `json:"session_files"`
}

type optionalSettingWire struct {
	Key      string `json:"key"`
	State    string `json:"state"`
	Declined bool   `json:"declined"`
}

type hookPolicyWire struct {
	Runs   bool   `json:"runs"`
	Reason string `json:"reason,omitempty"`
}

type sessionFilesWire struct {
	Err         string    `json:"err,omitempty"`
	Sessions    int       `json:"sessions"`
	LastReadAt  time.Time `json:"last_read_at,omitzero"`
	BytesBehind int64     `json:"bytes_behind"`
	Truncated   bool      `json:"truncated"`
	Ambiguous   int       `json:"ambiguous"`
	Unreadable  int       `json:"unreadable"`
	// Signals is counts and field names, the registry's own form.
	Signals drift.Signals `json:"signals,omitzero"`
	// Walked says whether the run that wrote this bundle paid for the
	// second reading of the project's tree. The three gap counts above
	// are that reading's when it is true, and the registry's record of
	// the older search when it is false.
	Walked bool `json:"walked"`
	// WalkErr is why the second reading could not run, when it was
	// asked for and failed.
	WalkErr string `json:"walk_err,omitempty"`
	// Unregistered counts the sessions the second reading found that
	// the registry does not hold. It is a count, like everything here:
	// naming one would name what the user worked on.
	Unregistered int `json:"unregistered"`
}

type proxyWire struct {
	Addr   string `json:"addr"`
	Holder string `json:"holder"`
	// Reason is present when the holder could not be proven ours.
	Reason string `json:"reason,omitempty"`
	// Health is present only when the holder is ours.
	Health any `json:"health,omitempty"`
	// IdleBetweenSessions says an absent proxy is the healthy state on
	// this device, because every enabled project records without one.
	// Without it a reader cannot tell that holder apart from a proxy
	// that should be running and is not.
	IdleBetweenSessions bool `json:"idle_between_sessions"`
}

type spoolWire struct {
	OpenErr     string             `json:"open_err,omitempty"`
	UsageBytes  int64              `json:"usage_bytes"`
	QuotaBytes  int64              `json:"quota_bytes"`
	WritableErr string             `json:"writable_err,omitempty"`
	Days        []spool.DaySummary `json:"days"`
	// OldestRecordAt is when the oldest record of either slot still
	// waiting was captured; with the day summaries it says how far
	// behind uploading is.
	OldestRecordAt time.Time `json:"oldest_record_at,omitzero"`
}

type rejectedWire struct {
	BatchID string           `json:"batch_id"`
	Records int              `json:"records"`
	Reason  upload.Rejection `json:"reason"`
}

type tokenStoreWire struct {
	Paired bool   `json:"paired"`
	Err    string `json:"err,omitempty"`
}

// InfoJSON is the bundle's identity entry: which build wrote it, on
// what platform, when, and about which proxy address.
func InfoJSON(d Diagnosis, generatedAt time.Time) []byte {
	return mustJSON(map[string]any{
		"version":      d.Version,
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"generated_at": generatedAt.UTC(),
		"proxy_addr":   d.Proxy.Addr,
	})
}

// DiagnosisJSON serializes a Diagnosis for the bundle.
func DiagnosisJSON(d Diagnosis) []byte {
	rejected := make([]rejectedWire, 0, len(d.Rejected))
	for _, b := range d.Rejected {
		rejected = append(rejected, rejectedWire{BatchID: b.BatchID, Records: b.Records, Reason: b.Reason})
	}
	days := d.Spool.Days
	if days == nil {
		days = []spool.DaySummary{}
	}
	proxy := proxyWire{
		Addr:                d.Proxy.Addr,
		Holder:              d.Proxy.Holder.String(),
		Reason:              errString(d.Proxy.Reason),
		IdleBetweenSessions: d.ProxyIdleBetweenSessions,
	}
	if d.Proxy.Holder == proxylife.HolderOurs {
		proxy.Health = d.Proxy.Health
	}
	return mustJSON(diagnosisWire{
		Project: projectWire{
			Root:             d.Project.Root,
			ProjectIDHash:    d.Project.Hash,
			Enabled:          d.Project.Enabled,
			Upstream:         maskUpstreamCredentials(d.Project.Upstream),
			Injected:         d.Project.Injected,
			InjectedToken:    maskedToken(d.Project.InjectedToken),
			Token:            maskedToken(d.Project.Token),
			HooksInstalled:   d.Project.HookInstalled,
			SessionEndOK:     d.Project.SessionEndInstalled,
			GitSnapshotOK:    d.Project.GitSnapshotInstalled,
			AgreementVersion: d.Project.AgreementVersion,
			ConsentState:     string(d.Project.ConsentState),
			PauseReason:      string(d.Project.PauseReason),
			NoProxy:          d.Project.Shape == routing.WithoutProxy,
			WindowsSide:      d.Project.WindowsSideClaude,
			HookPolicy:       hookPolicyValue(d),
			OptionalSettings: optionalSettingValues(d),
			SessionFiles: sessionFilesWire{
				Err:          errString(d.SessionFiles.Err),
				Sessions:     d.SessionFiles.Sessions,
				LastReadAt:   d.SessionFiles.LastReadAt,
				BytesBehind:  d.SessionFiles.BytesBehind,
				Truncated:    d.SessionFiles.Gaps.Truncated,
				Ambiguous:    len(d.SessionFiles.Gaps.Ambiguous),
				Unreadable:   len(d.SessionFiles.Gaps.Unreadable),
				Signals:      d.SessionFiles.Signals,
				Walked:       d.SessionFiles.Walked,
				WalkErr:      errString(d.SessionFiles.WalkErr),
				Unregistered: d.SessionFiles.Unregistered,
			},
		},
		Proxy: proxy,
		Spool: spoolWire{
			OpenErr:        errString(d.Spool.OpenErr),
			UsageBytes:     d.Spool.Usage,
			QuotaBytes:     d.Spool.Quota,
			WritableErr:    errString(d.Spool.WritableErr),
			Days:           days,
			OldestRecordAt: d.Spool.OldestRecord,
		},
		Uploads:     d.Uploads,
		Rejected:    rejected,
		RejectedErr: errString(d.RejectedErr),
		Handshake:   d.Handshake,
		Standings:   d.Standings,
		TokenStore:  tokenStoreWire{Paired: d.TokenStore.Paired, Err: errString(d.TokenStore.Err)},
		Selfcheck:   selfcheckValue(d),

		StaleDiscoveryHook: d.StaleDiscoveryHook,
	})
}

// hookPolicyValue carries the static reading when one was taken.
func hookPolicyValue(d Diagnosis) *hookPolicyWire {
	if d.HookPolicy == nil {
		return nil
	}
	return &hookPolicyWire{Runs: d.HookPolicy.Runs, Reason: d.HookPolicy.Reason}
}

// settingStateNames spells each optional setting state for the wire. A
// state this table does not name reaches the bundle as unknown rather
// than as one of the states it does name.
var settingStateNames = map[claudesettings.SettingState]string{
	claudesettings.Unset:     "unset",
	claudesettings.OnByUs:    "on_by_us",
	claudesettings.OnByUser:  "on_by_user",
	claudesettings.OffByUser: "off_by_user",
}

// optionalSettingValues carries each optional setting's state, nil when
// the diagnosis resolved none.
func optionalSettingValues(d Diagnosis) []optionalSettingWire {
	if len(d.OptionalSettings) == 0 {
		return nil
	}
	out := make([]optionalSettingWire, 0, len(d.OptionalSettings))
	for _, s := range d.OptionalSettings {
		name, ok := settingStateNames[s.State]
		if !ok {
			name = "unknown"
		}
		out = append(out, optionalSettingWire{Key: s.Key, State: name, Declined: s.Declined})
	}
	return out
}

// selfcheckValue keeps a nil *Selfcheck out of the JSON instead of
// marshalling it as null.
func selfcheckValue(d Diagnosis) any {
	if d.Selfcheck == nil {
		return nil
	}
	return *d.Selfcheck
}

// RoutingJSON serializes the routing table for the bundle with every
// token masked.
func RoutingJSON(paused routing.PauseReason, grants []routing.Grant) []byte {
	type project struct {
		RootPath      string      `json:"root_path"`
		ProjectIDHash string      `json:"project_id_hash"`
		Upstream      string      `json:"upstream"`
		GrantedAt     string      `json:"granted_at"`
		Revoked       bool        `json:"revoked"`
		Token         maskedToken `json:"token"`
	}
	summary := struct {
		PausedReason string    `json:"paused_reason"`
		Projects     []project `json:"projects"`
	}{PausedReason: string(paused), Projects: []project{}}
	for _, g := range grants {
		summary.Projects = append(summary.Projects, project{
			RootPath:      g.RootPath,
			ProjectIDHash: g.ProjectIDHash,
			Upstream:      maskUpstreamCredentials(g.Upstream),
			GrantedAt:     g.GrantedAt,
			Revoked:       g.Revoked,
			Token:         maskedToken(g.Token),
		})
	}
	return mustJSON(summary)
}

// maskedUpstream stands in for an upstream that could not be masked
// piece by piece. It is deliberately not a URL: a reader must not
// mistake it for a destination the project actually used.
const maskedUpstream = "redacted (upstream could not be parsed)"

// maskUpstreamCredentials strips the credentials a user-configured relay
// URL may carry — userinfo and query values — before the upstream is
// written into a bundle the user shares. The host and path stay so the
// diagnosis still shows where traffic was routed.
//
// A value url.Parse refuses is replaced whole rather than returned
// unchanged. Go's parser is stricter than the one Claude Code uses, so
// "not a URL" here routinely means a working relay URL whose password
// holds a '|' or a bare '%' — the very case that carries a credential.
// Returning it verbatim was the same fail-open maskQuery had until
// 2026-08-22, one layer out: the bundle is the one artifact that leaves
// this machine, and the command hands it over saying it holds no
// credentials. What cannot be masked selectively goes entirely; that
// the value was there and could not be read is itself the diagnosis.
// 2026-08-24.
func maskUpstreamCredentials(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return maskedUpstream
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	if u.RawQuery != "" {
		u.RawQuery = maskQuery(u.RawQuery)
	}
	return u.String()
}

// maskedQuery stands in for a query string that could not be masked pair
// by pair. It is not a legal key=value pair on purpose: a reader must not
// mistake it for something the upstream actually carried.
const maskedQuery = "redacted"

// maskQuery replaces every query value with a placeholder.
//
// The decision keys off the raw query, never off how many pairs
// url.Values managed to parse. Query discards ParseQuery's error and
// silently skips any pair it cannot unescape — a bare '%' in a value, a
// ';' anywhere in one — so a query whose pairs all fail parsed as "no
// query at all", the masking loop never ran, and RawQuery rode into the
// bundle verbatim. The bundle is the one artifact that leaves this
// machine, and the command hands it over saying it holds no credentials,
// so a relay key in `?token=Ab3;Xy9` left with it. That is the same
// fail-open-on-a-parsing-quirk redaction hit in hasDatabaseURLSecret on
// 2026-08-14: a query that cannot be parsed cannot be masked
// selectively, so the whole of it goes. 2026-08-22.
func maskQuery(rawQuery string) string {
	q, err := url.ParseQuery(rawQuery)
	if err != nil || len(q) == 0 {
		return maskedQuery
	}
	for k := range q {
		q.Set(k, maskedQuery)
	}
	return q.Encode()
}

// maskToken keeps just enough of a token to correlate entries across
// the bundle without disclosing it.
func maskToken(token string) string {
	if len(token) <= 8 {
		if token == "" {
			return ""
		}
		return "masked"
	}
	return token[:8] + "…(masked)"
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Every value marshalled here is built from plain maps and
		// structs; failure is a programming error.
		panic(err)
	}
	return append(data, '\n')
}
