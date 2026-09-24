package report

import "fmt"

// RecordingState is what this device captures right now, as one value:
// the dashboard's opening line, the line enable ends with, and the line
// a running session is told all read it, so the three can never answer
// the question differently.
//
// The device-wide states come before the one about this project. An
// unreadable routing table, a pause and a full spool stop every project
// on the machine, and a device that stops everywhere must not read as
// a project that is merely not enabled.
type RecordingState int

const (
	// RecordingOn is this project contributing with nothing device-wide
	// in the way.
	RecordingOn RecordingState = iota
	// RecordingTableUnreadable is a routing table that exists and
	// cannot be read. No token resolves, so no project on this device
	// captures anything, and whether a pause stands is unknown.
	RecordingTableUnreadable
	// RecordingPausedDeviceWide is a pause of any reason. No project on
	// this device captures anything while it stands.
	RecordingPausedDeviceWide
	// RecordingSpoolFull is a spool that reached its quota. Nothing can
	// be written anywhere on this device until something leaves it.
	RecordingSpoolFull
	// RecordingOffHere is a working device with no standing grant for
	// the project the command ran in.
	RecordingOffHere
)

// StoppedDeviceWide reports a state in which no project on this device
// captures anything. It is what a running session is told about: which
// of them holds decides nothing for that session, because each way the
// session's own project is as unrecorded as every other.
func (s RecordingState) StoppedDeviceWide() bool {
	return s == RecordingTableUnreadable || s == RecordingPausedDeviceWide || s == RecordingSpoolFull
}

// Recording decides the state from a diagnosis. It reads the
// device-wide facts and this project's injection and nothing else, so
// a caller holding only those — a session hook, which may not pay for
// a whole dashboard on every turn — asks this same question and gets
// this same answer.
func Recording(d Diagnosis) RecordingState {
	switch {
	case d.Project.TableUnreadable != nil:
		return RecordingTableUnreadable
	case d.Project.PauseReason != "":
		return RecordingPausedDeviceWide
	case d.Spool.full():
		return RecordingSpoolFull
	case !d.Project.InjectionAgrees:
		return RecordingOffHere
	default:
		return RecordingOn
	}
}

// Verdict is the one line that answers the question a user opens
// status with: is anything of mine captured right now. status leads
// with it and enable ends with it.
func Verdict(state RecordingState, projects int) string {
	switch state {
	case RecordingPausedDeviceWide:
		return "Recording: PAUSED on this device"
	case RecordingSpoolFull:
		return "Recording: STOPPED on this device (spool full)"
	case RecordingTableUnreadable:
		return "Recording: STOPPED on this device (routing table unreadable)"
	case RecordingOffHere:
		return "Recording: off in this project"
	default:
		// A project read as contributing is at least one, whatever the
		// count said: a line reading "on (0 project(s))" answers the
		// question with a contradiction.
		return fmt.Sprintf("Recording: on (%d project(s))", max(projects, 1))
	}
}
