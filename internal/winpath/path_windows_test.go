package winpath

import "testing"

func TestExtendedPaths(t *testing.T) {
	for input, want := range map[string]string{
		`C:\work\project`:              `\\?\C:\work\project`,
		`C:/work/project`:              `\\?\C:\work\project`,
		`\\server\share\project`:       `\\?\UNC\server\share\project`,
		`\\?\C:\work\project`:          `\\?\C:\work\project`,
		`\\?\UNC\server\share\project`: `\\?\UNC\server\share\project`,
	} {
		got, err := Extended(input)
		if err != nil || got != want {
			t.Errorf("Extended(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}
