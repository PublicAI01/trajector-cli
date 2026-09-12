package claudesettings

import "os"

// SettingState classifies an optional setting's effective value across
// the settings chain.
type SettingState int

const (
	// Unset: no rank sets the key. Distinct from OffByUser — the
	// behavior is the same, but "never said" and "explicitly turned
	// off" get opposite suggested answers.
	Unset SettingState = iota
	// OnByUs: the project-local value is the true trajector wrote.
	OnByUs
	// OnByUser: an effective true trajector did not write.
	OnByUser
	// OffByUser: an explicit false, wherever it comes from.
	OffByUser
)

// SettingStatus pairs the state with the rank the deciding value came
// from. Source is meaningful only for OnByUser and OffByUser.
type SettingStatus struct {
	State  SettingState
	Source Source
}

// ClassifySetting resolves a top-level boolean key the way Claude Code
// does — project-local, then project, then user settings; top-level
// keys have no shell rank — and classifies the outcome. A bare bool
// carries no shape that could mark it as trajector's own writing, so
// writtenByUs brings that fact in from the caller's records.
func ClassifySetting(projectRoot string, host Host, key string, writtenByUs bool) SettingStatus {
	d, found := host.chain(projectRoot, nil, layerProject|layerUser).
		decide([]string{key}, topLevelBoolValue, anyValue)
	switch {
	case !found:
		return SettingStatus{State: Unset}
	case !d.value.(bool):
		return SettingStatus{State: OffByUser, Source: d.source}
	case d.source == SourceProjectLocal && writtenByUs:
		return SettingStatus{State: OnByUs}
	default:
		return SettingStatus{State: OnByUser, Source: d.source}
	}
}

// TopLevelBool reads a top-level boolean key from the settings file at
// path. found separates an explicit false from an absent key; a
// missing or unreadable file reads as absent, as does a value of any
// other type.
func TopLevelBool(path, key string) (value, found bool) {
	raw, found := topLevelBoolValue(readRoot(path), key)
	if !found {
		return false, false
	}
	return raw.(bool), true
}

// SetTopLevelBool writes value under the top-level key in the settings
// file at path, preserving all other content.
func SetTopLevelBool(path, key string, value bool) error {
	return edit(path, func(root map[string]any) error {
		root[key] = value
		return nil
	})
}

// RestoreTopLevelBool puts the top-level key back to its recorded
// prior state: deleted when priorFound is false, priorValue written
// back otherwise. It acts only while the key still holds wrote, the
// value trajector set — anything else is a later hand edit, and the
// user's most recent decision wins, so the file is left byte-for-byte
// as it is. The check and the restore share one locked edit so no
// concurrent writer can slip between them. A missing file stays
// missing.
func RestoreTopLevelBool(path, key string, wrote, priorValue, priorFound bool) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return edit(path, func(root map[string]any) error {
		if current, ok := root[key].(bool); !ok || current != wrote {
			return errUnchanged
		}
		if !priorFound {
			delete(root, key)
			return nil
		}
		root[key] = priorValue
		return nil
	})
}
