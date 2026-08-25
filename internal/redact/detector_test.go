package redact

import (
	"errors"
	"testing"

	"github.com/betterleaks/betterleaks/detect"
)

// stubDetector makes the pattern layer fail to build for the rest of
// this test, and puts the real constructor back afterwards.
func stubDetector(t *testing.T, err error) {
	t.Helper()
	reset := func() {
		betterleaksDetectorMu.Lock()
		defer betterleaksDetectorMu.Unlock()
		betterleaksDetector, betterleaksDetectorErr, betterleaksDetectorSet = nil, nil, false
	}
	prev := newDetector
	newDetector = func() (*detect.Detector, error) { return nil, err }
	reset()
	t.Cleanup(func() { newDetector = prev; reset() })
}

// TestJSONLBytesRefusesWhenThePatternDetectorIsUnavailable pins the
// fail-closed rule for the one layer nothing else backs up. The
// betterleaks layer carries 260+ known credential formats; its
// construction error used to be swallowed inside a sync.Once, and every
// caller then read a nil detector as "skip this layer". Nothing logged
// it, nothing counted it, and batch.Build still handed back
// RedactedBytes — the type whose whole job is to prove at compile time
// that unredacted data cannot reach an upload.
func TestJSONLBytesRefusesWhenThePatternDetectorIsUnavailable(t *testing.T) {
	want := errors.New("the detector config could not be built")
	stubDetector(t, want)

	out, err := JSONLBytes([]byte(`{"note":"anything at all"}`))
	if err == nil {
		t.Fatalf("JSONLBytes certified %q as redacted with a whole detection layer missing", out.Bytes())
	}
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want it to carry %v", err, want)
	}
	if out.Len() != 0 {
		t.Errorf("a refused call still handed back %d byte(s)", out.Len())
	}
}
