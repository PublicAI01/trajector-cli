package claudesettings

import "regexp"

// windowsDriveMount matches a path on a Windows drive mounted into WSL.
// Only drive-letter mounts count: /mnt/data on a plain Linux host is an
// ordinary volume, and /mnt/wsl is WSL's own.
var windowsDriveMount = regexp.MustCompile(`^/mnt/[A-Za-z](?:/|$)`)

// windowsDrivePath matches a path that starts with a drive letter.
var windowsDrivePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// WindowsSideClaude reports that Claude Code runs on the Windows side
// of a WSL boundary: the project lives on a Windows drive mounted into
// WSL, or a session file path has a drive letter or backslashes. Hooks
// fired from that side run Windows commands and cannot reach a
// trajector installed in WSL.
func WindowsSideClaude(projectRoot, sessionFilePath string) bool {
	return windowsDriveMount.MatchString(projectRoot) || windowsShaped(sessionFilePath)
}

func windowsShaped(path string) bool {
	if windowsDrivePath.MatchString(path) {
		return true
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' {
			return true
		}
	}
	return false
}
