package report_test

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

const tablePath = "/home/dev/.config/trajector/proxy_projects.json"

func unreadableTable(cause error) *routing.UnreadableError {
	return &routing.UnreadableError{Path: tablePath, Err: cause}
}

func TestAnUnreadableRoutingTableStopsTheDeviceAndNamesTheFileOnEverySurface(t *testing.T) {
	d := device()
	d.Project.TableUnreadable = unreadableTable(errors.New("unexpected end of JSON input"))
	d.Project.Injected = true

	if got := report.Recording(d); got != report.RecordingTableUnreadable || !got.StoppedDeviceWide() {
		t.Errorf("Recording = %v, want the unreadable table, which stops the whole device", got)
	}
	named := []string{
		"recording is stopped: the routing table could not be read",
		"reading " + tablePath + " failed (unexpected end of JSON input)",
		"Move that file aside yourself; no command of trajector's moves or rewrites it.",
		"For a project that sends its traffic through a relay: write the relay's URL back to ANTHROPIC_BASE_URL in its .claude/settings.local.json. An earlier copy of the routing table records it as that project's `upstream`.",
		"For a project that uses the official endpoint: delete that ANTHROPIC_BASE_URL line.",
		"Then run `trajector enable` in each project you enabled.",
	}

	status := dashboard(d)
	if first, _, _ := strings.Cut(status, "\n"); first != "Recording: STOPPED on this device (routing table unreadable)" {
		t.Errorf("status opened with %q, want the verdict for an unreadable table", first)
	}
	wants(t, "status", status, named...)
	wants(t, "status", status, "Whether this project is enabled is unknown while the routing table cannot be read")
	rejects(t, "status", status, "fix:", "Not enabled", "disagree")
	if errs := report.Dashboard(&bytes.Buffer{}, report.Style{}, d); errs != 1 {
		t.Errorf("status counted %d error(s), want the table once", errs)
	}

	problems, doctor := doctorText(d)
	if problems != 1 {
		t.Errorf("doctor counted %d problem(s), want the table once", problems)
	}
	wants(t, "doctor", doctor, named...)
	rejects(t, "doctor", doctor, "fix:")

	var alone bytes.Buffer
	report.TableUnreadable(&alone, report.Style{}, d.Project.TableUnreadable)
	wants(t, "a command that could not read the table", alone.String(), named...)
}

func TestAnUnreadableRoutingTableNamesItsPathOnce(t *testing.T) {
	d := device()
	d.Project.TableUnreadable = unreadableTable(&fs.PathError{Op: "read", Path: tablePath, Err: errors.New("is a directory")})

	why := ""
	for line := range strings.SplitSeq(dashboard(d), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "why:"); ok {
			why = rest
		}
	}
	if !strings.Contains(why, "(is a directory)") || strings.Count(why, tablePath) != 1 {
		t.Errorf("why = %q, want the path once and the failure without it", why)
	}
}
