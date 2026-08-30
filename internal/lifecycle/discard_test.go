package lifecycle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestDiscardDeletesAQuarantinedBatch(t *testing.T) {
	e := newEnv(t)
	at := e.deps.Now()
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison", Details: "413 Request Entity Too Large"},
		map[string][]byte{
			"req-1": spooledEnvelope(t, "req-1", at),
			"req-2": spooledEnvelope(t, "req-2", at),
		})

	if err := e.machine().DiscardRejected("b-poison", false, false, e.io()); err != nil {
		t.Fatalf("discard: %v\nstdout: %s", err, e.stdout)
	}

	if _, err := os.Stat(filepath.Join(e.layout().RejectedDir(), "b-poison")); !os.IsNotExist(err) {
		t.Error("rejected batch directory still present after discard")
	}
	if got := len(e.sandbox.Rawcalls()); got != 0 {
		t.Errorf("spool holds %d rawcall(s) after discard, want the records gone, not requeued", got)
	}
	out := e.stdout.String()
	for _, want := range []string{"2 rawcall(s)", "b-poison", "413 Request Entity Too Large"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
}

func TestDiscardDeletesRecordsRequeueRefusesToMove(t *testing.T) {
	e := newEnv(t)
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-torn", Details: "never sent: unreadable in the spool (bad envelope)"},
		map[string][]byte{
			"req-bad": []byte("not an envelope"),
		})

	if err := e.machine().RequeueRejected("b-torn", false, e.io()); err == nil {
		t.Fatal("precondition: requeue must keep refusing an unreadable record")
	}
	e.stdout.Reset()

	if err := e.machine().DiscardRejected("b-torn", false, false, e.io()); err != nil {
		t.Fatalf("discard: %v\nstdout: %s", err, e.stdout)
	}
	if _, err := os.Stat(filepath.Join(e.layout().RejectedDir(), "b-torn")); !os.IsNotExist(err) {
		t.Error("a locally quarantined batch survived discard")
	}
	if !strings.Contains(e.stdout.String(), "1 rawcall(s)") {
		t.Errorf("output = %q, want the deleted record counted", e.stdout)
	}
}

func TestDiscardDropsTheQuarantineWarningFromStatusAndDoctor(t *testing.T) {
	e := newEnv(t)
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison"},
		map[string][]byte{
			"req-1": spooledEnvelope(t, "req-1", e.deps.Now()),
		})
	if problems, _ := e.doctor(); problems == 0 {
		t.Fatal("precondition: a quarantined batch is a doctor problem")
	}

	e.stdout.Reset()
	if err := e.machine().DiscardRejected("b-poison", false, true, e.io()); err != nil {
		t.Fatalf("discard: %v", err)
	}

	e.stdout.Reset()
	if !strings.Contains(e.statusOutput(), "Never uploaded") {
		t.Fatalf("status did not render after discard: %q", e.stdout)
	}
	if strings.Contains(e.stdout.String(), "quarantined") {
		t.Errorf("status = %q, want no quarantine warning left", e.stdout)
	}
	e.stdout.Reset()
	problems, out := e.doctor()
	if problems != 0 {
		t.Errorf("problems = %d after discarding the only quarantined batch, output:\n%s", problems, out)
	}
	if !strings.Contains(out, "no rejected batches quarantined") {
		t.Errorf("doctor = %q, want an empty quarantine reported", out)
	}
}

func TestDiscardUnknownBatchFails(t *testing.T) {
	e := newEnv(t)
	err := e.machine().DiscardRejected("b-missing", false, true, e.io())
	if err == nil || !strings.Contains(err.Error(), "b-missing") {
		t.Fatalf("err = %v, want the unknown batch named", err)
	}
}

func TestDiscardRefusesABatchIdThatLeavesTheQuarantine(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(e.layout().RejectedDir(), "..", "spool")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().DiscardRejected(filepath.Join("..", "spool"), false, true, e.io()); err == nil {
		t.Fatal("discard accepted a batch id pointing outside the quarantine")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a directory outside the quarantine was deleted: %v", err)
	}
}

func TestDiscardAllEmptiesTheQuarantine(t *testing.T) {
	e := newEnv(t)
	at := e.deps.Now()
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-one"},
		map[string][]byte{"req-1": spooledEnvelope(t, "req-1", at)})
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-two"},
		map[string][]byte{"req-2": spooledEnvelope(t, "req-2", at)})

	if err := e.machine().DiscardRejected("", true, false, e.io()); err != nil {
		t.Fatalf("discard --all: %v\nstdout: %s", err, e.stdout)
	}
	if entries, err := os.ReadDir(e.layout().RejectedDir()); err == nil && len(entries) != 0 {
		t.Errorf("rejected store not empty after discard --all: %v", entries)
	}
	if !strings.Contains(e.stdout.String(), "Deleted 2 quarantined rawcall(s)") {
		t.Errorf("output = %q, want every deleted record counted", e.stdout)
	}
}

func TestDiscardAllWithNothingQuarantinedSaysSo(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().DiscardRejected("", true, false, e.io()); err != nil {
		t.Fatalf("discard --all on empty store: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "No rejected batches") {
		t.Errorf("output = %q, want it to say there is nothing to discard", e.stdout)
	}
}

func TestDiscardWithoutAnAnswerDeletesNothing(t *testing.T) {
	e := newEnv(t)
	e.stdin = ""
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison"},
		map[string][]byte{
			"req-1": spooledEnvelope(t, "req-1", e.deps.Now()),
		})

	if err := e.machine().DiscardRejected("b-poison", false, false, e.io()); err != nil {
		t.Fatalf("discard without an answer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.layout().RejectedDir(), "b-poison")); err != nil {
		t.Errorf("the batch was deleted without a yes: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "Nothing was discarded") {
		t.Errorf("output = %q, want the refusal reported", e.stdout)
	}
}

func TestDiscardConfirmationFlagSkipsThePrompt(t *testing.T) {
	e := newEnv(t)
	e.stdin = ""
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison"},
		map[string][]byte{
			"req-1": spooledEnvelope(t, "req-1", e.deps.Now()),
		})

	if err := e.machine().DiscardRejected("b-poison", false, true, e.io()); err != nil {
		t.Fatalf("discard --yes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.layout().RejectedDir(), "b-poison")); !os.IsNotExist(err) {
		t.Error("the batch survived a confirmed discard")
	}
	if strings.Contains(e.stdout.String(), "[y/N]") {
		t.Errorf("output = %q, want no prompt with the confirmation flag", e.stdout)
	}
}
