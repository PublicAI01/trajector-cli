package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// The rejected store holds rawcalls of batches the service refused as
// unacceptable, moved out of the spool so one bad batch cannot block
// every upload behind it. The layout is a documented product contract:
//
//	<dir>/<batch_id>/<request_id>.json   the rawcall, exactly as spooled
//	<dir>/<batch_id>/reason.json         why the batch was rejected
//
// Nothing here is deleted automatically: a rejected batch may still
// correspond to compensation, so it waits — loudly, via status and
// doctor — for a fixed client to requeue it or the user to discard it.
const reasonName = "reason.json"

// Rejection describes one rejected batch, both inside reason.json and
// in the uploader state read by status.
type Rejection struct {
	BatchID string    `json:"batch_id"`
	Records int       `json:"records"`
	Details string    `json:"details,omitempty"`
	At      time.Time `json:"at"`
}

// quarantine moves one rejected batch's records from the spool into the
// rejected store. The copy lands before the spool deletes, so a failure
// anywhere leaves every record present in at least one of the two
// places; rerunning after a partial move overwrites the same files.
func quarantine(rejectedDir string, sp *spool.Spool, rej Rejection, rawcalls []spool.Rawcall) error {
	dir := filepath.Join(rejectedDir, rej.BatchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, rc := range rawcalls {
		if err := os.WriteFile(filepath.Join(dir, rc.RequestID+".json"), rc.Data, 0o600); err != nil {
			return err
		}
	}
	reason, err := json.Marshal(rej)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, reasonName), append(reason, '\n'), 0o600); err != nil {
		return err
	}

	moved := map[string]bool{}
	for _, rc := range rawcalls {
		moved[rc.RequestID] = true
	}
	_, err = sp.DeleteWhere(func(r spool.Rawcall) bool { return moved[r.RequestID] })
	return err
}

// PurgeRejected deletes a project's rawcalls from the rejected store,
// for consent withdrawal: rejected records are still local unuploaded
// data and withdrawal must reach them too. Batch directories left empty
// of records are removed with their reason files.
func PurgeRejected(rejectedDir, projectIDHash string) (int, error) {
	batches, err := os.ReadDir(rejectedDir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, b := range batches {
		if !b.IsDir() {
			continue
		}
		dir := filepath.Join(rejectedDir, b.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return deleted, err
		}
		remaining := 0
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || name == reasonName || filepath.Ext(name) != ".json" {
				continue
			}
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				return deleted, err
			}
			hash, ok := envelope.ProjectIDHashOf(data)
			if !ok || hash != projectIDHash {
				remaining++
				continue
			}
			if err := os.Remove(path); err != nil {
				return deleted, err
			}
			deleted++
		}
		if remaining == 0 {
			if err := os.RemoveAll(dir); err != nil {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

// RejectedCount reports how many rawcalls sit in the rejected store,
// for surfaces that must warn while any are waiting.
func RejectedCount(rejectedDir string) (int, error) {
	batches, err := os.ReadDir(rejectedDir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, b := range batches {
		if !b.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(rejectedDir, b.Name()))
		if err != nil {
			return count, err
		}
		for _, f := range files {
			if !f.IsDir() && f.Name() != reasonName && filepath.Ext(f.Name()) == ".json" {
				count++
			}
		}
	}
	return count, nil
}

// errRejected is what a flush returns after quarantining a batch: the
// flush stops, but the queue behind the batch is no longer blocked.
type errRejected struct {
	rej Rejection
	dir string
}

func (e *errRejected) Error() string {
	return fmt.Sprintf("the service rejected batch %s; %d rawcall(s) moved to %s and will not be retried automatically (%s)",
		e.rej.BatchID, e.rej.Records, e.dir, e.rej.Details)
}
