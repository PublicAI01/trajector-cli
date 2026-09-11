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

// The schema_version 2 fixtures carry more than an answer: they carry
// the record stream and index both sides must agree on. What this
// proves, per fixture: the index decodes into this client's type and
// serializes back to the same fields in the same order; every transcript
// record decodes into its type and serializes back byte for byte; every
// record id can be recomputed from the record's identity; and the index
// routes by source exactly as the stream is laid out. A rawcall record
// is read through the record version this client still writes, and is
// not compared byte for byte: the fixture spells an empty anthropic-beta
// list where this client omits the field.
func assertV2Fixture(t *testing.T, c conformance.Case) {
	t.Helper()
	ix, err := batch.ParseIndexV2(c.EnvelopeBytes)
	if err != nil {
		t.Fatalf("batch.json does not decode as a schema_version 2 index: %v", err)
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
			assertV2Rawcall(t, i, line, item)
		case envelope.KindSegment:
			assertV2Segment(t, i, line, item)
		case envelope.KindMetaSnapshot:
			assertV2MetaSnapshot(t, i, line, item)
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
		if ix.Records[i-1].Source > ix.Records[i].Source {
			t.Errorf("record %d: source %q follows %q; the stream groups sources", i, ix.Records[i].Source, ix.Records[i-1].Source)
		}
	}
}

func assertV2Rawcall(t *testing.T, i int, line []byte, item batch.IndexItemV2) {
	t.Helper()
	// The record envelope's version gate is unchanged in this round, so
	// the fixture's record is read through the version it still writes.
	asV1 := bytes.Replace(line, []byte(`"schema_version":"2"`), []byte(`"schema_version":"1"`), 1)
	env, err := envelope.Parse(asV1)
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

func assertV2Segment(t *testing.T, i int, line []byte, item batch.IndexItemV2) {
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

func assertV2MetaSnapshot(t *testing.T, i int, line []byte, item batch.IndexItemV2) {
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

// TestV2FixturesRoundTripThroughTheUploader drives every schema_version
// 2 fixture through this client's own upload path: its records are
// stored in the spool as the capture side would store them, flushed
// through the uploader to the fake service, and what the service
// received is compared with the fixture — the index item by item, the
// stream record by record. The fixture records are already redacted, so
// the packing pass must return them byte for byte; a rawcall is read
// through the record version this client still writes.
func TestV2FixturesRoundTripThroughTheUploader(t *testing.T) {
	ran := 0
	for _, c := range sharedFixtures(t) {
		if c.Envelope["schema_version"] != "2" {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
			want, err := batch.ParseIndexV2(c.EnvelopeBytes)
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
			if got.SchemaVersion != "2" || got.Compression != want.Compression || got.RecordsSize != want.RecordsSize || int64(len(stream)) != want.RecordsSize {
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
		t.Fatal("no schema_version 2 fixture ran")
	}
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
		asV1 := bytes.Replace(line, []byte(`"schema_version":"2"`), []byte(`"schema_version":"1"`), 1)
		env, err := envelope.Parse(asV1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.spool.Write(env); err != nil {
			t.Fatal(err)
		}
		return asV1
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
	default:
		t.Fatalf("fixture record declares %+v, which this client cannot store", kind)
	}
	return line
}
