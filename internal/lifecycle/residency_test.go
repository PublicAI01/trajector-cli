package lifecycle

import (
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/report"
)

func TestAllProjectsWithoutProxy(t *testing.T) {
	tests := []struct {
		name     string
		projects []report.ProjectStatus
		want     bool
	}{
		{
			name:     "no projects at all",
			projects: nil,
			want:     false,
		},
		{
			name:     "one enabled project without the proxy",
			projects: []report.ProjectStatus{{Enabled: true, NoProxy: true}},
			want:     true,
		},
		{
			name:     "every enabled project without the proxy",
			projects: []report.ProjectStatus{{Enabled: true, NoProxy: true}, {Enabled: true, NoProxy: true}},
			want:     true,
		},
		{
			name:     "one enabled project still routes through the proxy",
			projects: []report.ProjectStatus{{Enabled: true, NoProxy: true}, {Enabled: true, NoProxy: false}},
			want:     false,
		},
		{
			name:     "a disabled project routing does not count against it",
			projects: []report.ProjectStatus{{Enabled: true, NoProxy: true}, {Enabled: false, NoProxy: false}},
			want:     true,
		},
		{
			name:     "only disabled projects",
			projects: []report.ProjectStatus{{Enabled: false, NoProxy: true}},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allProjectsWithoutProxy(tt.projects); got != tt.want {
				t.Errorf("allProjectsWithoutProxy = %v, want %v", got, tt.want)
			}
		})
	}
}
