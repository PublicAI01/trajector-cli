package batch_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/mediablock"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// constructedPNG is a few dozen bytes of valid PNG, never a real
// screenshot.
func constructedPNG(t *testing.T, shade uint8) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 4, 3))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func pngBlock(payload string) string {
	return `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}`
}

// callWithImage is a recorded call whose request carries the payload in
// a tool result, the way a call repeats an image the session read.
func callWithImage(t *testing.T, id, sessionKey, payload string, at time.Time) spool.Entry {
	t.Helper()
	return storedRawcall(t, id, sessionKey, "hash-p1",
		`{"model":"m","metadata":{"user_id":"`+sessionKey+`"},"messages":[{"role":"user","content":[`+
			`{"type":"tool_result","tool_use_id":"toolu_01","content":[`+pngBlock(payload)+`]}]}]}`,
		`{"id":"`+id+`","type":"message","content":[{"type":"thinking","thinking":"","signature":"`+fakeSignature+`"}]}`, at)
}

// lineWithImage is the session line that carries the same tool result.
func lineWithImage(payload string) string {
	return `{"type":"user","sessionId":"sess-1","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_01","content":[` + pngBlock(payload) + `]}]}}` + "\n"
}

// packedBodies returns each packed record's body in stream order: a
// rawcall as it is, a segment as its lines.
func packedBodies(t *testing.T, b batch.Batch) []string {
	t.Helper()
	stream := decompress(t, b.Records.Bytes())
	var bodies []string
	for _, item := range parseIndex(t, b).Records {
		raw := stream[item.Offset : item.Offset+item.Size]
		if item.Source != envelope.KindSegment.Source {
			bodies = append(bodies, string(raw))
			continue
		}
		var seg envelope.Segment
		if err := json.Unmarshal(raw, &seg); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, seg.Lines)
	}
	return bodies
}

var sourcePattern = regexp.MustCompile(`"source":\{[^{}]*\}`)

func sourcesIn(body string) []string {
	return sourcePattern.FindAllString(body, -1)
}

func digest(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func TestBuild_BothRecordingPathsOmitABlockInOneShape(t *testing.T) {
	x := constructedPNG(t, 40)
	in := spool.Entries{
		callWithImage(t, "req-1", "sess-1", x, buildTime),
		storedSegment(t, "sess-1", "", 0, buildTime, lineWithImage(x)),
	}
	b, refused, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{}, mediablock.Policy{Omit: true})
	if err != nil || len(refused) != 0 {
		t.Fatalf("Build = %v, refused %+v", err, refused)
	}
	bodies := packedBodies(t, b)
	proxy, transcript := sourcesIn(bodies[0]), sourcesIn(bodies[1])
	if len(proxy) != 1 || len(transcript) != 1 {
		t.Fatalf("sources: proxy %v, transcript %v", proxy, transcript)
	}
	want := `"source":{"type":"omitted","media_type":"image/png","sha256":"` + digest(x) + `","bytes":`
	if proxy[0] != transcript[0] || !bytes.HasPrefix([]byte(proxy[0]), []byte(want)) {
		t.Fatalf("the two paths left different placeholders:\n%s\n%s", proxy[0], transcript[0])
	}
	if !bytes.Contains([]byte(proxy[0]), []byte(`"width":4,"height":3}`)) {
		t.Fatalf("the placeholder does not state the image's size: %s", proxy[0])
	}
	for _, body := range bodies {
		if bytes.Contains([]byte(body), []byte(x)) {
			t.Fatal("a payload left the machine with upload turned off")
		}
	}
	if len(b.Originals) != 0 {
		t.Fatalf("Originals = %v, want none with upload turned off", b.Originals)
	}
}

func TestBuild_ALaterRecordOfASessionRefersToAnEarlierRecordOfTheBatch(t *testing.T) {
	x := constructedPNG(t, 40)
	in := spool.Entries{
		callWithImage(t, "req-2", "sess-1", x, buildTime.Add(time.Second)),
		callWithImage(t, "req-1", "sess-1", x, buildTime),
		callWithImage(t, "req-3", "sess-2", x, buildTime),
		storedSegment(t, "sess-1", "", 0, buildTime, lineWithImage(x)),
		storedSegment(t, "sess-1", "", 1, buildTime, lineWithImage(x)),
	}
	b, _, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{}, mediablock.Policy{Sent: func(string, string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, body := range packedBodies(t, b) {
		src := sourcesIn(body)[0]
		switch {
		case bytes.Contains([]byte(src), []byte(`"type":"base64"`)):
			kinds = append(kinds, "original")
		case bytes.Contains([]byte(src), []byte(`"type":"sha256_ref","media_type":"image/png","sha256":"`+digest(x)+`"}`)):
			kinds = append(kinds, "reference")
		default:
			t.Fatalf("unexpected source %s", src)
		}
	}
	// Stream order: sess-1's calls oldest first, then sess-2's, then
	// the two segments of sess-1.
	want := []string{"original", "reference", "original", "original", "reference"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("copies = %v, want %v: one original per session and path, the rest references", kinds, want)
	}
	if len(b.Originals) != 3 {
		t.Fatalf("Originals = %v, want one per scope", b.Originals)
	}
}

func TestBuild_ARefusedRecordLendsItsPayloadToNoOtherRecord(t *testing.T) {
	x := constructedPNG(t, 40)
	cut := storedSegment(t, "sess-1", "", 0, buildTime, lineWithImage(x)[:len(lineWithImage(x))-1])
	kept := storedSegment(t, "sess-1", "", 1, buildTime, lineWithImage(x))
	b, refused, err := batch.Build("batch-1", buildTime, "test", spool.Entries{cut, kept}, batch.Run{},
		mediablock.Policy{Sent: func(string, string) bool { return false }})
	if err != nil || len(refused) != 1 {
		t.Fatalf("Build = %v, refused %+v; want the cut segment refused", err, refused)
	}
	if src := sourcesIn(packedBodies(t, b)[0]); !bytes.Contains([]byte(src[0]), []byte(`"type":"base64"`)) {
		t.Fatalf("the packed record refers to a payload that only a refused record carried: %s", src[0])
	}
}

func TestBuild_ThePayloadsAnAcknowledgedBatchCarriedAreReferredTo(t *testing.T) {
	x, y := constructedPNG(t, 40), constructedPNG(t, 80)
	sent := func(scope, d string) bool { return d == digest(x) }
	in := spool.Entries{callWithImage(t, "req-1", "sess-1", x, buildTime), callWithImage(t, "req-2", "sess-1", y, buildTime.Add(time.Second))}
	b, _, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{}, mediablock.Policy{Sent: sent})
	if err != nil {
		t.Fatal(err)
	}
	bodies := packedBodies(t, b)
	if !bytes.Contains([]byte(bodies[0]), []byte(`"sha256_ref"`)) || bytes.Contains([]byte(bodies[0]), []byte(x)) {
		t.Fatal("a payload the service holds went up in full")
	}
	if !bytes.Contains([]byte(bodies[1]), []byte(y)) {
		t.Fatal("a payload the service does not hold went up as a reference")
	}
	if len(b.Originals) != 1 || b.Originals[0].Digest != digest(y) {
		t.Fatalf("Originals = %v, want only the payload carried in full", b.Originals)
	}
	for _, body := range bodies {
		if !bytes.Contains([]byte(body), []byte(`"signature":"`+fakeSignature+`"`)) {
			t.Fatal("a signature did not survive the rewrite byte for byte")
		}
	}
}

// noisyPNG is a constructed image whose base64 reads as random text,
// the kind the entropy layer masks parts of when it scans it.
func noisyPNG(t *testing.T) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 24, 16))
	state := uint32(2463534242)
	for i := range img.Pix {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		img.Pix[i] = uint8(state)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestBuild_OmitsTheReadResultCopyASessionLineKeeps(t *testing.T) {
	x := noisyPNG(t)
	line := `{"type":"user","sessionId":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[` +
		pngBlock(x) + `]}]},"toolUseResult":{"type":"image","file":{"base64":"` + x + `","type":"image/png","originalSize":70}}}` + "\n"
	in := spool.Entries{storedSegment(t, "sess-1", "", 0, buildTime, line)}
	b, _, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{}, mediablock.Policy{Omit: true})
	if err != nil {
		t.Fatal(err)
	}
	body := packedBodies(t, b)[0]
	if bytes.Contains([]byte(body), []byte(x)) {
		t.Fatal("the Read result's copy left the machine with upload turned off")
	}
	if !bytes.Contains([]byte(body), []byte(`"file":{"sha256":"`+digest(x)+`","bytes":`)) ||
		!bytes.Contains([]byte(body), []byte(`"width":24,"height":16,"type":"image/png"`)) {
		t.Fatalf("the Read result carries no placeholder of its content:\n%s", body)
	}
	if got := sourcesIn(body); len(got) != 1 || !bytes.Contains([]byte(got[0]), []byte(digest(x))) {
		t.Fatalf("the block and the Read result do not name one content: %v", got)
	}
}

func TestBuild_WithUploadOnTheReadResultCopyIsMaskedAsText(t *testing.T) {
	x := noisyPNG(t)
	line := `{"type":"user","sessionId":"sess-1","toolUseResult":{"type":"image","file":{"base64":"` + x + `","type":"image/png"}}}` + "\n"
	b, _, err := batch.Build("batch-1", buildTime, "test", spool.Entries{storedSegment(t, "sess-1", "", 0, buildTime, line)}, batch.Run{},
		mediablock.Policy{Sent: func(string, string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	body := packedBodies(t, b)[0]
	if bytes.Contains([]byte(body), []byte("sha256")) || !bytes.Contains([]byte(body), []byte("REDACTED")) {
		t.Fatalf("a Read result was rewritten, or no longer passed through redaction:\n%.300s", body)
	}
}
