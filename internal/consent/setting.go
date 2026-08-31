package consent

import "fmt"

// SettingAnswer is the user's recorded answer to an optional Claude
// Code setting offered during enable.
type SettingAnswer string

const (
	AnswerAccepted SettingAnswer = "accepted"
	AnswerDeclined SettingAnswer = "declined"
)

// SettingPrior is what the project's configuration held before an
// accepted setting was applied. PriorTrue means the value was already
// true and nothing was written, so disable must leave it alone; only
// PriorAbsent and PriorFalse mark a write of ours to undo. Merging
// these states would make disable delete a value the user set.
type SettingPrior string

const (
	PriorAbsent SettingPrior = "absent"
	PriorFalse  SettingPrior = "false"
	PriorTrue   SettingPrior = "true"
)

// SettingDecision is one setting's recorded answer.
type SettingDecision struct {
	Answer SettingAnswer `json:"answer"`
	// Prior is set exactly when the answer is accepted.
	Prior     SettingPrior `json:"prior,omitempty"`
	DecidedAt string       `json:"decided_at"`
}

// SetSettingDecision records the user's answer for one optional
// setting. The project must already have a recorded decision; a
// setting answer without one has no enable to belong to.
func (s *Store) SetSettingDecision(projectIDHash, settingKey string, d SettingDecision) error {
	if projectIDHash == "" {
		return fmt.Errorf("consent: project hash is required")
	}
	if settingKey == "" {
		return fmt.Errorf("consent: setting key is required")
	}
	switch d.Answer {
	case AnswerAccepted:
		switch d.Prior {
		case PriorAbsent, PriorFalse, PriorTrue:
		default:
			return fmt.Errorf("consent: accepted setting decision needs a prior state, got %q", d.Prior)
		}
	case AnswerDeclined:
		if d.Prior != "" {
			return fmt.Errorf("consent: declined setting decision cannot carry a prior state")
		}
	default:
		return fmt.Errorf("consent: unknown setting answer %q", d.Answer)
	}
	var missing bool
	err := s.update(func(f *storeFile) {
		rec, ok := f.Projects[projectIDHash]
		if !ok {
			missing = true
			return
		}
		if rec.Settings == nil {
			rec.Settings = map[string]SettingDecision{}
		}
		rec.Settings[settingKey] = d
		f.Projects[projectIDHash] = rec
	})
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("consent: no project record to attach a setting decision to")
	}
	return nil
}

// SettingDecisions reports a project's recorded setting decisions by
// setting key; a project with none yields an empty map.
func (s *Store) SettingDecisions(projectIDHash string) (map[string]SettingDecision, error) {
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	return f.Projects[projectIDHash].Settings, nil
}

// ClearSettingDecision removes one setting's recorded decision,
// leaving the project's state and its other decisions in place.
// Call it only after the setting was successfully restored: the
// record is the only way left to undo the write, so a failed restore
// must never clear. Unknown projects and settings are a no-op.
func (s *Store) ClearSettingDecision(projectIDHash, settingKey string) error {
	if projectIDHash == "" {
		return fmt.Errorf("consent: project hash is required")
	}
	if settingKey == "" {
		return fmt.Errorf("consent: setting key is required")
	}
	return s.update(func(f *storeFile) {
		rec, ok := f.Projects[projectIDHash]
		if !ok {
			return
		}
		delete(rec.Settings, settingKey)
		if len(rec.Settings) == 0 {
			rec.Settings = nil
		}
		f.Projects[projectIDHash] = rec
	})
}
