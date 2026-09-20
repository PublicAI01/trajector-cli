package report_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func TestRecordingAnswersTheOneQuestionStatusOpensWith(t *testing.T) {
	tests := []struct {
		name     string
		pause    routing.PauseReason
		writable error
		here     bool
		projects int
		state    report.RecordingState
		want     string
	}{
		{
			name:     "contributing device",
			here:     true,
			projects: 3,
			state:    report.RecordingOn,
			want:     "Recording: on (3 project(s))",
		},
		{
			name:     "paused device",
			pause:    routing.PauseSignedOut,
			here:     true,
			projects: 3,
			state:    report.RecordingPausedDeviceWide,
			want:     "Recording: PAUSED on this device",
		},
		{
			name:     "spool at its quota",
			writable: spool.ErrQuotaExceeded,
			here:     true,
			projects: 3,
			state:    report.RecordingSpoolFull,
			want:     "Recording: STOPPED on this device (spool full)",
		},
		{
			name:     "spool at its quota in a project that never enabled",
			writable: spool.ErrQuotaExceeded,
			projects: 2,
			state:    report.RecordingSpoolFull,
			want:     "Recording: STOPPED on this device (spool full)",
		},
		{
			name:     "spool refusing a write for another reason",
			writable: errors.New("permission denied"),
			here:     true,
			projects: 1,
			state:    report.RecordingOn,
			want:     "Recording: on (1 project(s))",
		},
		{
			name:     "paused device with a full spool",
			pause:    routing.PauseSignedOut,
			writable: spool.ErrQuotaExceeded,
			here:     true,
			projects: 3,
			state:    report.RecordingPausedDeviceWide,
			want:     "Recording: PAUSED on this device",
		},
		{
			name:     "project that never enabled",
			projects: 2,
			state:    report.RecordingOffHere,
			want:     "Recording: off in this project",
		},
		{
			name:     "count the routing table refused to give",
			here:     true,
			projects: 0,
			state:    report.RecordingOn,
			want:     "Recording: on (1 project(s))",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := device()
			d.Project.PauseReason = tt.pause
			d.Project.InjectionAgrees = tt.here
			d.Spool.WritableErr = tt.writable
			d.EnabledProjects = tt.projects
			state := report.Recording(d)
			if state != tt.state {
				t.Fatalf("Recording = %v, want %v", state, tt.state)
			}
			if got := report.Verdict(state, tt.projects); got != tt.want {
				t.Errorf("Verdict = %q, want %q", got, tt.want)
			}
			if got := dashboard(d); !strings.HasPrefix(got, tt.want+"\n") {
				t.Errorf("status opened with %q, want the verdict %q", strings.SplitN(got, "\n", 2)[0], tt.want)
			}
		})
	}
}

func TestOnlyADeviceWideStopIsSaidToARunningSession(t *testing.T) {
	stopped := map[report.RecordingState]bool{
		report.RecordingOn:               false,
		report.RecordingPausedDeviceWide: true,
		report.RecordingSpoolFull:        true,
		report.RecordingOffHere:          false,
	}
	for state, want := range stopped {
		if got := state.StoppedDeviceWide(); got != want {
			t.Errorf("%v.StoppedDeviceWide() = %v, want %v", state, got, want)
		}
	}
}
