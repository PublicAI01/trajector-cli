package upload_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/upload"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
)

// The fixtures that carry a record stream carry more than an answer:
// they carry the stream and the index both sides must agree on. What
// this proves, per fixture: the index decodes into this client's type
// and serializes back to the same fields in the same order; every
// record of the second slot decodes into its type and serializes back
// byte for byte; every record id can be recomputed from the record's
// identity; and the index routes by source exactly as the stream is
// laid out. A rawcall record is not compared byte for byte: a fixture
// may spell an empty anthropic-beta list where this client omits the
// field.
func assertStreamFixture(t *testing.T, c conformance.Case) {
	t.Helper()
	ix, err := batch.ParseIndex(c.EnvelopeBytes)
	if err != nil {
		t.Fatalf("batch.json does not decode as an index this client reads: %v", err)
	}
	got, err := ix.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, c.EnvelopeBytes); err != nil {
		t.Fatal(err)
	}
	if string(got) != want.String() {
		t.Errorf("index serialized differently from the fixture:\n got %s\nwant %s", got, want.String())
	}
	if len(ix.Records) != len(c.Records) {
		t.Fatalf("index has %d items, stream has %d records", len(ix.Records), len(c.Records))
	}

	var offset int64
	bySource := map[string][]string{}
	for i, line := range c.Records {
		item := ix.Records[i]
		if item.Offset != offset || item.Size != int64(len(line)) {
			t.Errorf("record %d: index says offset %d size %d, stream has offset %d size %d", i, item.Offset, item.Size, offset, len(line))
		}
		offset += int64(len(line))
		bySource[item.Source] = append(bySource[item.Source], item.RecordID)

		kind, err := envelope.KindOf(line)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if kind.Source != item.Source {
			t.Errorf("record %d: index says source %q, record says %q", i, item.Source, kind.Source)
		}
		switch kind {
		case envelope.KindRawcall:
			assertFixtureRawcall(t, i, line, item)
		case envelope.KindSegment:
			assertFixtureSegment(t, i, line, item)
		case envelope.KindMetaSnapshot:
			assertFixtureMetaSnapshot(t, i, line, item)
		case envelope.KindGitSnapshot:
			assertFixtureGitSnapshot(t, i, line, item)
		default:
			t.Errorf("record %d declares itself %+v, which this client cannot read", i, kind)
		}
	}

	routed, err := fakeplatform.RecordIDsBySource(c.EnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(routed) != len(bySource) {
		t.Errorf("routing groups = %v, want %v", routed, bySource)
	}
	for source, ids := range bySource {
		if strings.Join(routed[source], ",") != strings.Join(ids, ",") {
			t.Errorf("routing for %q = %v, want %v", source, routed[source], ids)
		}
	}
	for i := 1; i < len(ix.Records); i++ {
		if sourceOrder(t, ix.Records[i-1].Source) > sourceOrder(t, ix.Records[i].Source) {
			t.Errorf("record %d: source %q follows %q; the stream groups sources in the contract's order", i, ix.Records[i].Source, ix.Records[i-1].Source)
		}
	}
}

// sourceOrder is where one source sits in the stream. The order is the
// contract's own and not the alphabet's, so it is stated here rather
// than compared as text.
func sourceOrder(t *testing.T, source string) int {
	t.Helper()
	order := map[string]int{
		envelope.KindRawcall.Source:     0,
		envelope.KindSegment.Source:     1,
		envelope.KindGitSnapshot.Source: 2,
	}
	at, ok := order[source]
	if !ok {
		t.Fatalf("the index names source %q, which has no place in the stream order", source)
	}
	return at
}

// indexed reports a fixture whose envelope indexes its records by
// source, which is the shape this client reads. The earliest fixtures
// predate that key and are read for their answers alone.
func indexed(c conformance.Case) bool {
	if len(c.Records) == 0 {
		return false
	}
	_, err := batch.ParseIndex(c.EnvelopeBytes)
	return err == nil
}

func assertFixtureRawcall(t *testing.T, i int, line []byte, item batch.IndexItem) {
	t.Helper()
	env, err := envelope.Parse(line)
	if err != nil {
		t.Errorf("record %d: %v", i, err)
		return
	}
	if env.RequestID() != item.RecordID {
		t.Errorf("record %d: record_id %q is not the request_id %q", i, item.RecordID, env.RequestID())
	}
	if item.ProjectIDHash != env.ProjectIDHash() || item.UpstreamOrigin != env.UpstreamOrigin() || item.Endpoint != env.Endpoint() || item.Garbled != env.Garbled() {
		t.Errorf("record %d: index item %+v does not match the rawcall it indexes", i, item)
	}
	var stored struct {
		Capture map[string]json.RawMessage `json:"capture"`
	}
	if err := json.Unmarshal(line, &stored); err != nil {
		t.Fatal(err)
	}
	raw, present := stored.Capture["upstream_request_id"]
	if present != (env.UpstreamRequestID() != "") {
		t.Errorf("record %d: upstream_request_id present=%v but read back as %q", i, present, env.UpstreamRequestID())
	}
	if present && string(raw) == `""` {
		t.Errorf("record %d: upstream_request_id is an empty placeholder", i)
	}
	if strings.HasPrefix(env.RequestID(), "local-") && present {
		t.Errorf("record %d: a locally named rawcall carries an upstream id %s", i, raw)
	}
}

func assertFixtureSegment(t *testing.T, i int, line []byte, item batch.IndexItem) {
	t.Helper()
	seg, err := envelope.ParseSegment(line)
	if err != nil {
		t.Errorf("record %d: %v", i, err)
		return
	}
	again, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, line) {
		t.Errorf("record %d: segment serialized differently from the fixture:\n got %s\nwant %s", i, again, line)
	}
	if want := envelope.SegmentRecordID(seg.SessionID, seg.File, seg.SegmentIndex); seg.RecordID != want || item.RecordID != want {
		t.Errorf("record %d: record_id %q (index %q), recomputed %q", i, seg.RecordID, item.RecordID, want)
	}
	if item.ProjectIDHash != seg.Capture.ProjectIDHash || item.Timestamp != seg.Capture.Timestamp || item.UpstreamOrigin != "" || item.Endpoint != "" {
		t.Errorf("record %d: index item %+v does not match the segment's capture %+v", i, item, seg.Capture)
	}
	if !strings.HasSuffix(seg.Lines, "\n") {
		t.Errorf("record %d: lines do not end in a newline", i)
	}
	for n, l := range strings.Split(strings.TrimSuffix(seg.Lines, "\n"), "\n") {
		if l == "" {
			t.Errorf("record %d: line %d is empty", i, n)
			continue
		}
		for _, sig := range signaturesIn(t, l) {
			if !bytes.Contains(again, []byte(`\"signature\":\"`+sig+`\"`)) {
				t.Errorf("record %d: line %d: signature %q did not survive serialization", i, n, sig)
			}
		}
	}
}

func assertFixtureMetaSnapshot(t *testing.T, i int, line []byte, item batch.IndexItem) {
	t.Helper()
	snap, err := envelope.ParseMetaSnapshot(line)
	if err != nil {
		t.Errorf("record %d: %v", i, err)
		return
	}
	again, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, line) {
		t.Errorf("record %d: snapshot serialized differently from the fixture:\n got %s\nwant %s", i, again, line)
	}
	want, err := envelope.MetaSnapshotRecordID(snap.SessionID, snap.File, snap.Content)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RecordID != want || item.RecordID != want {
		t.Errorf("record %d: record_id %q (index %q), recomputed %q", i, snap.RecordID, item.RecordID, want)
	}
	if item.ProjectIDHash != snap.Capture.ProjectIDHash || item.Timestamp != snap.Capture.Timestamp || item.UpstreamOrigin != "" || item.Endpoint != "" {
		t.Errorf("record %d: index item %+v does not match the snapshot's capture %+v", i, item, snap.Capture)
	}
}

func assertFixtureGitSnapshot(t *testing.T, i int, line []byte, item batch.IndexItem) {
	t.Helper()
	snap, err := envelope.ParseGitSnapshot(line)
	if err != nil {
		t.Errorf("record %d: %v", i, err)
		return
	}
	again, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, line) {
		t.Errorf("record %d: git snapshot serialized differently from the fixture:\n got %s\nwant %s", i, again, line)
	}
	want := envelope.GitSnapshotRecordID(snap.SessionID, snap.HookEvent, snap.Head, snap.Capture.Timestamp)
	if snap.RecordID != want || item.RecordID != want {
		t.Errorf("record %d: record_id %q (index %q), recomputed %q", i, snap.RecordID, item.RecordID, want)
	}
	if item.ProjectIDHash != snap.Capture.ProjectIDHash || item.Timestamp != snap.Capture.Timestamp || item.UpstreamOrigin != "" || item.Endpoint != "" {
		t.Errorf("record %d: index item %+v does not match the observation's capture %+v", i, item, snap.Capture)
	}
	// The two statements the bound makes about itself: a truncated
	// record carries exactly the bound and counts past it, and an
	// untruncated one counts exactly what it carries.
	if snap.Truncated != (len(snap.Changed) == envelope.MaxChanges && snap.ChangedCount > envelope.MaxChanges) {
		t.Errorf("record %d: truncated=%v with %d of %d changes", i, snap.Truncated, len(snap.Changed), snap.ChangedCount)
	}
	if !snap.Truncated && snap.ChangedCount != len(snap.Changed) {
		t.Errorf("record %d: changed_count %d with %d changes carried", i, snap.ChangedCount, len(snap.Changed))
	}
	if snap.Base == nil && len(snap.Changed) != 0 {
		t.Errorf("record %d: %d changes with nothing to compare against", i, len(snap.Changed))
	}
}

// sameInstantOrDiff returns "" when the two timestamps name the same
// instant, so the caller's field comparison passes, and the differing
// value otherwise.
func sameInstantOrDiff(t *testing.T, i int, client, fixture string) string {
	t.Helper()
	a, errA := time.Parse(time.RFC3339Nano, client)
	b, errB := time.Parse(time.RFC3339Nano, fixture)
	if errA != nil || errB != nil {
		t.Errorf("record %d: timestamps %q / %q: %v %v", i, client, fixture, errA, errB)
		return fixture
	}
	if !a.Equal(b) {
		return fixture
	}
	return ""
}

// signaturesIn lists the signatures of the content blocks in one
// transcript line, in order. A line whose content is a plain string has
// no blocks.
func signaturesIn(t *testing.T, line string) []string {
	t.Helper()
	var v struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		t.Fatalf("transcript line is not JSON: %v", err)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(v.Message.Content), []byte("[")) {
		return nil
	}
	var blocks []struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(v.Message.Content, &blocks); err != nil {
		t.Fatalf("content blocks are not a list: %v", err)
	}
	var sigs []string
	for _, block := range blocks {
		if block.Signature != "" {
			sigs = append(sigs, block.Signature)
		}
	}
	return sigs
}

// TestFixturesRoundTripThroughTheUploader drives every fixture that
// carries a record stream through this client's own upload path: its
// records are stored in the spool as the capture side would store them,
// flushed through the uploader to the fake service, and what the
// service received is compared with the fixture — the index item by
// item, the stream record by record. The fixture records are already
// redacted, so the packing pass must return them byte for byte.
//
// The batch this client builds declares the current version whatever
// version the fixture was written at, and so does every record in it: a
// batch and its records carry one version number. That is the whole of
// what a fixture from an earlier version may differ by, which is why
// the wanted bytes are the fixture's with that one token restated.
func TestFixturesRoundTripThroughTheUploader(t *testing.T) {
	ran := 0
	for _, c := range sharedFixtures(t) {
		if !indexed(c) {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
			want, err := batch.ParseIndex(c.EnvelopeBytes)
			if err != nil {
				t.Fatal(err)
			}
			wantBodies := make([][]byte, len(c.Records))
			for i, line := range c.Records {
				wantBodies[i] = f.storeFixtureRecord(t, line)
			}

			res, err := f.uploader.Flush(true)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != upload.Uploaded || res.Batches != 1 || res.Records != len(c.Records) {
				t.Fatalf("result = %+v, want one batch of %d records", res, len(c.Records))
			}
			reqs := f.server.Requests()
			if len(reqs) != 1 {
				t.Fatalf("service saw %d requests, want 1", len(reqs))
			}
			got := uploadedIndex(t, reqs[0])
			stream := uploadedStream(t, reqs[0])
			if got.SchemaVersion != envelope.SchemaVersion || got.Compression != want.Compression || int64(len(stream)) != got.RecordsSize {
				t.Errorf("envelope = %s/%s/%d with a %d byte stream, fixture says %s/%s/%d", got.SchemaVersion, got.Compression, got.RecordsSize, len(stream), want.SchemaVersion, want.Compression, want.RecordsSize)
			}
			if len(got.Records) != len(want.Records) {
				t.Fatalf("index has %d items, fixture has %d:\n%s", len(got.Records), len(want.Records), mustPart(t, reqs[0], "batch"))
			}
			for i, w := range want.Records {
				g := got.Records[i]
				g.Timestamp, w.Timestamp = sameInstantOrDiff(t, i, g.Timestamp, w.Timestamp), ""
				if g != w {
					t.Errorf("item %d:\n got %+v\nwant %+v", i, g, w)
				}
				body := stream[g.Offset : g.Offset+g.Size]
				if !bytes.Equal(body, wantBodies[i]) {
					t.Errorf("record %d differs from the fixture:\n got %s\nwant %s", i, body, wantBodies[i])
				}
			}
			if f.spool.Usage() != 0 {
				t.Errorf("spool holds %d bytes after the ack", f.spool.Usage())
			}
		})
	}
	if ran == 0 {
		t.Fatal("no fixture carrying a record stream ran")
	}
}

// atCurrentVersion is the fixture record as this client must ship it:
// the same bytes with the schema version restated, because a batch and
// the records in it declare one version.
func atCurrentVersion(t *testing.T, line []byte) []byte {
	t.Helper()
	var declared struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(line, &declared); err != nil {
		t.Fatal(err)
	}
	from := []byte(`{"schema_version":"` + declared.SchemaVersion + `"`)
	to := []byte(`{"schema_version":"` + envelope.SchemaVersion + `"`)
	if !bytes.HasPrefix(line, from) {
		t.Fatalf("fixture record does not open with its schema version: %s", line)
	}
	return append(to, line[len(from):]...)
}

// storeFixtureRecord stores one fixture record in the slot its kind
// names and returns the bytes the upload must carry for it.
func (f *fixture) storeFixtureRecord(t *testing.T, line []byte) []byte {
	t.Helper()
	kind, err := envelope.KindOf(line)
	if err != nil {
		t.Fatal(err)
	}
	switch kind {
	case envelope.KindRawcall:
		env, err := envelope.Parse(line)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.spool.Write(env); err != nil {
			t.Fatal(err)
		}
	case envelope.KindSegment:
		seg, err := envelope.ParseSegment(line)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.spool.WriteSegment(seg); err != nil {
			t.Fatal(err)
		}
	case envelope.KindMetaSnapshot:
		snap, err := envelope.ParseMetaSnapshot(line)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.spool.WriteMetaSnapshot(snap); err != nil {
			t.Fatal(err)
		}
	case envelope.KindGitSnapshot:
		snap, err := envelope.ParseGitSnapshot(line)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.spool.WriteGitSnapshot(snap); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("fixture record declares %+v, which this client cannot store", kind)
	}
	return atCurrentVersion(t, line)
}
