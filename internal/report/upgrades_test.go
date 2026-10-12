package report_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

var lastCheck = time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)

// autoUpgradesLineOf is the one line of out that is about automatic
// upgrades, with its indent and marker left on.
func autoUpgradesLineOf(t *testing.T, surface, out string) string {
	t.Helper()
	var found []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(strings.ToLower(line), "automatic upgrades:") {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s has %d lines about automatic upgrades, want one:\n%s", surface, len(found), out)
	}
	return found[0]
}

func TestStatusAlwaysSaysHowTrajectorKeepsItselfUpToDate(t *testing.T) {
	cases := []struct {
		name  string
		state report.AutoUpgradeState
		want  string
	}{
		{
			"up to date",
			report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultNewest},
			"Automatic upgrades: last checked 2026-10-12T08:30:00Z, up to date.",
		},
		{
			"upgraded",
			report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultUpgraded, Installed: "0.4.0"},
			"Automatic upgrades: last checked 2026-10-12T08:30:00Z, upgraded to 0.4.0.",
		},
		{
			"managed",
			report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultManaged, Manager: "Homebrew"},
			"Automatic upgrades: last checked 2026-10-12T08:30:00Z, managed by Homebrew.",
		},
		{
			"managed by a manager no name is known for",
			report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultManaged},
			"Automatic upgrades: last checked 2026-10-12T08:30:00Z, managed by a package manager.",
		},
		{
			"a result a newer build wrote",
			report.AutoUpgradeState{CheckedAt: lastCheck, Result: "deferred"},
			"Automatic upgrades: last checked 2026-10-12T08:30:00Z.",
		},
		{
			"turned off",
			report.AutoUpgradeState{Off: true, CheckedAt: lastCheck, Result: selfupdate.ResultFailed, Error: "x"},
			"Automatic upgrades: off (config.json).",
		},
		{
			"failed",
			report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultFailed, Error: "fetching the release index: 503 Service Unavailable."},
			"Automatic upgrades: the last attempt at 2026-10-12T08:30:00Z failed: fetching the release index: 503 Service Unavailable. Run `trajector upgrade`.",
		},
		{
			"failed for no recorded reason",
			report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultFailed},
			"Automatic upgrades: the last attempt at 2026-10-12T08:30:00Z failed. Run `trajector upgrade`.",
		},
		{
			"config file unreadable",
			report.AutoUpgradeState{ConfigErr: errors.New("unexpected end of JSON input"), Off: true},
			"Automatic upgrades: off (config.json could not be read).",
		},
		{
			"never checked",
			report.AutoUpgradeState{},
			"Automatic upgrades: on; no check has run yet.",
		},
		{
			// A build from source never checks, whatever it last found
			// when it was a release build and whatever the file says.
			"not a release build",
			report.AutoUpgradeState{NotRelease: true, ConfigErr: errors.New("cut"), CheckedAt: lastCheck, Result: selfupdate.ResultFailed, Error: "x"},
			"Automatic upgrades: off (not a release build).",
		},
		{
			"not a release build with a state file it cannot read",
			report.AutoUpgradeState{NotRelease: true, StateErr: errors.New("cut")},
			"Automatic upgrades: off (not a release build).",
		},
		{
			"state file unreadable",
			report.AutoUpgradeState{StateErr: errors.New("reading upgrade.json: invalid character")},
			"Automatic upgrades: on; what the last check found could not be read: reading upgrade.json: invalid character.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := device()
			d.AutoUpgrades = tc.state
			var out strings.Builder
			// Nothing about upgrading stops recording, so nothing here
			// changes what status exits with.
			if problems := report.Dashboard(&out, report.Style{}, d); problems != 0 {
				t.Errorf("problems = %d, want 0", problems)
			}
			if got := autoUpgradesLineOf(t, "status", out.String()); !strings.HasSuffix(got, tc.want) {
				t.Errorf("status says %q, want %q", got, tc.want)
			}
			if device, _, _ := strings.Cut(out.String(), "\nProject "); !strings.Contains(device, tc.want) {
				t.Errorf("the line is not in the Device section:\n%s", out.String())
			}
		})
	}
}

func TestDoctorStatesAFailedUpgradeAsAProblemWithItsFix(t *testing.T) {
	d := device()
	d.AutoUpgrades = report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultFailed, Error: "fetching the release index: 503 Service Unavailable"}

	problems, out := doctorText(d)
	if problems != 1 {
		t.Errorf("problems = %d, want the failed attempt counted:\n%s", problems, out)
	}
	wants(t, "doctor", out,
		"automatic upgrades: the last attempt at 2026-10-12T08:30:00Z failed\n",
		"    why:  fetching the release index: 503 Service Unavailable\n",
		"    fix:  trajector upgrade\n")
}

func TestDoctorStatesEveryOtherUpgradeStateAndCountsNoneOfThem(t *testing.T) {
	cases := map[string]struct {
		state report.AutoUpgradeState
		want  string
	}{
		"up to date":       {report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultNewest}, "automatic upgrades: last checked 2026-10-12T08:30:00Z, up to date"},
		"turned off":       {report.AutoUpgradeState{Off: true}, "automatic upgrades: off (config.json)"},
		"config file":      {report.AutoUpgradeState{ConfigErr: errors.New("cut")}, "automatic upgrades: off (config.json could not be read)"},
		"never checked":    {report.AutoUpgradeState{}, "automatic upgrades: on; no check has run yet"},
		"not a release":    {report.AutoUpgradeState{NotRelease: true, CheckedAt: lastCheck, Result: selfupdate.ResultFailed}, "automatic upgrades: off (not a release build)"},
		"state unreadable": {report.AutoUpgradeState{StateErr: errors.New("cut")}, "automatic upgrades: on; what the last check found could not be read: cut"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := device()
			d.AutoUpgrades = tc.state
			problems, out := doctorText(d)
			if problems != 0 {
				t.Errorf("problems = %d, want 0:\n%s", problems, out)
			}
			if got := autoUpgradesLineOf(t, "doctor", out); !strings.HasSuffix(got, tc.want) {
				t.Errorf("doctor says %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheBundleCarriesWhatTheLastUpgradeCheckFound(t *testing.T) {
	d := device()
	d.AutoUpgrades = report.AutoUpgradeState{CheckedAt: lastCheck, Result: selfupdate.ResultFailed, Error: "timed out"}

	wants(t, "diagnosis.json", string(report.DiagnosisJSON(d)),
		`"checked_at": "2026-10-12T08:30:00Z"`,
		`"result": "failed"`,
		`"error": "timed out"`,
	)
}
