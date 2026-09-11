package claudesettings

import "testing"

func TestWindowsSideClaude_ProjectMountOrSessionFilePathShape(t *testing.T) {
	tests := []struct {
		name            string
		projectRoot     string
		sessionFilePath string
		want            bool
	}{
		{
			name:            "linux project and linux session file",
			projectRoot:     "/home/u/repo",
			sessionFilePath: "/home/u/.claude/projects/-home-u-repo/abc.jsonl",
		},
		{
			name:        "project on a Windows drive mounted into WSL",
			projectRoot: "/mnt/c/Users/u/repo",
			want:        true,
		},
		{
			name:        "a bare drive mount",
			projectRoot: "/mnt/d",
			want:        true,
		},
		{
			name:        "an ordinary volume under mnt is not a Windows drive",
			projectRoot: "/mnt/data/repo",
		},
		{
			name:        "WSL's own mount is not a Windows drive",
			projectRoot: "/mnt/wsl/repo",
		},
		{
			name:            "session file path with a drive letter and backslashes",
			projectRoot:     "/home/u/repo",
			sessionFilePath: `C:\Users\u\.claude\projects\C--Users-u-repo\abc.jsonl`,
			want:            true,
		},
		{
			name:            "session file path with a drive letter and forward slashes",
			projectRoot:     "/home/u/repo",
			sessionFilePath: "C:/Users/u/.claude/projects/abc.jsonl",
			want:            true,
		},
		{
			name:            "UNC session file path",
			projectRoot:     "/home/u/repo",
			sessionFilePath: `\\wsl$\Ubuntu\home\u\.claude\projects\abc.jsonl`,
			want:            true,
		},
		{
			name:        "empty session file path adds nothing",
			projectRoot: "/home/u/repo",
		},
		{
			name: "both empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WindowsSideClaude(tt.projectRoot, tt.sessionFilePath); got != tt.want {
				t.Errorf("WindowsSideClaude(%q, %q) = %v, want %v", tt.projectRoot, tt.sessionFilePath, got, tt.want)
			}
		})
	}
}
