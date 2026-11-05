package cli

import (
	"runtime/debug"
	"testing"
)

func TestOnlyTheReleaseStampMakesAReleaseBuildWhateverVersionIsReported(t *testing.T) {
	recorded := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	cases := map[string]struct {
		stamp         string
		releaseStamp  string
		readBuildInfo func() (*debug.BuildInfo, bool)
		version       string
		release       bool
	}{
		"the release pipeline":                {"v0.3.7", "true", recorded("v0.3.6"), "v0.3.7", true},
		"scripts/build.sh":                    {"v0.3.7-3-g9a59fd7", "", recorded("(devel)"), "v0.3.7-3-g9a59fd7", false},
		"a release stamp other than true":     {"v0.3.7", "1", recorded("v0.3.7"), "v0.3.7", false},
		"go install":                          {unstampedVersion, "", recorded("v0.3.7"), "v0.3.7", false},
		"go build in a checkout":              {unstampedVersion, "", recorded("v0.3.8-0.20261012000000-9a59fd7aaaaa"), "v0.3.8-0.20261012000000-9a59fd7aaaaa", false},
		"a checkout with no version control":  {unstampedVersion, "", recorded("(devel)"), unstampedVersion, false},
		"no build information at all":         {unstampedVersion, "", none, unstampedVersion, false},
		"build information without a version": {unstampedVersion, "", recorded(""), unstampedVersion, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, release := resolveVersion(tc.stamp, tc.releaseStamp, tc.readBuildInfo)
			if got != tc.version || release != tc.release {
				t.Errorf("resolveVersion = %q, %v; want %q, %v", got, release, tc.version, tc.release)
			}
		})
	}
}

func TestOnlyATreeTheToolchainRecordedAsModifiedMakesAModifiedBuild(t *testing.T) {
	withSettings := func(settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "v0.3.7"}, Settings: settings}, true
		}
	}
	cases := map[string]struct {
		readBuildInfo func() (*debug.BuildInfo, bool)
		modified      bool
	}{
		"uncommitted changes":         {withSettings(debug.BuildSetting{Key: "vcs.revision", Value: "9a59fd7"}, debug.BuildSetting{Key: "vcs.modified", Value: "true"}), true},
		"a clean tree":                {withSettings(debug.BuildSetting{Key: "vcs.revision", Value: "9a59fd7"}, debug.BuildSetting{Key: "vcs.modified", Value: "false"}), false},
		"no version control recorded": {withSettings(debug.BuildSetting{Key: "GOOS", Value: "linux"}), false},
		"no build information at all": {func() (*debug.BuildInfo, bool) { return nil, false }, false},
		"a value other than true":     {withSettings(debug.BuildSetting{Key: "vcs.modified", Value: "1"}), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := resolveBuildModified(tc.readBuildInfo); got != tc.modified {
				t.Errorf("resolveBuildModified = %v, want %v", got, tc.modified)
			}
		})
	}
}
