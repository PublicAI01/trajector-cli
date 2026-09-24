package follow_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

// segmentBound is the most lines one segment holds, in bytes, unless
// one line alone is longer.
const segmentBound = 8 << 20

// sizedLine is an assistant line of message id whose length, newline
// included, is size.
func sizedLine(id string, size int) string {
	return assistantLine(id, 0, strings.Repeat("x", size-len(assistantLine(id, 0, ""))))
}

// wantSegment is wantLines for lines too long to print: it reports
// sizes, not content.
func wantSegment(t *testing.T, res follow.ReadResult, want ...string) {
	t.Helper()
	got := segmentLines(t, res)
	if strings.Join(got, "") == strings.Join(want, "") && len(got) == len(want) {
		return
	}
	t.Errorf("segment holds %d lines of %v bytes, want %d lines of %v bytes", len(got), lineSizes(got), len(want), lineSizes(want))
}

func lineSizes(lines []string) []int {
	sizes := make([]int, len(lines))
	for i, l := range lines {
		sizes[i] = len(l)
	}
	return sizes
}

func TestReader_ReadsALargeFileInSegmentsThatJoinBackToIt(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	var content strings.Builder
	for i := range 7 {
		content.WriteString(userLine(i))
		content.WriteString(sizedLine("msg_"+string(rune('a'+i)), 3<<20))
	}
	writeFile(t, path, content.String())
	a.register(path, "")
	a.answer = follow.Stored

	a.reader.Advance(path)

	var joined strings.Builder
	for i, res := range a.handed {
		if len(res.Segments) != 1 {
			t.Fatalf("read %d handed %d segments, want one", i, len(res.Segments))
		}
		seg := res.Segments[0]
		if seg.SegmentIndex != i {
			t.Errorf("read %d segment index = %d, want %d", i, seg.SegmentIndex, i)
		}
		if len(seg.Lines) > segmentBound {
			t.Errorf("segment %d holds %d bytes, want at most %d", i, len(seg.Lines), segmentBound)
		}
		joined.WriteString(seg.Lines)
	}
	if len(a.handed) < 2 {
		t.Errorf("reads handed to the store = %d, want the file split into several segments", len(a.handed))
	}
	if joined.String() != content.String() {
		t.Errorf("segments joined hold %d bytes, want the %d bytes of the file line for line", joined.Len(), content.Len())
	}
	if f := a.entry(path); f.Offset != int64(content.Len()) || f.NextSegment != len(a.handed) {
		t.Errorf("cursor = offset %d, next segment %d, want the end of the file after %d segments", f.Offset, f.NextSegment, len(a.handed))
	}
}

func TestRead_ASegmentHoldsLinesUpToTheBound(t *testing.T) {
	half := sizedLine("msg_a", segmentBound/2)
	fills := sizedLine("msg_b", segmentBound/2)
	passes := sizedLine("msg_b", segmentBound/2+1)
	tests := []struct {
		name    string
		lines   []string
		want    []string
		wantIDs []string
	}{
		{"lines that fill the bound exactly", []string{half, fills, userLine(1)}, []string{half, fills}, []string{"msg_a", "msg_b"}},
		{"lines one byte past the bound", []string{half, passes, userLine(1)}, []string{half}, []string{"msg_a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := mainPath(t)
			writeFile(t, path, strings.Join(tt.lines, ""))

			res := read(t, follow.File{Path: path})

			wantSegment(t, res, tt.want...)
			if !res.More {
				t.Error("More = false, want the rest left for the next read")
			}
			wantCursor(t, res.File, int64(len(strings.Join(tt.want, ""))), 1, tt.wantIDs...)
		})
	}
}

func TestRead_ALineLongerThanTheBoundIsASegmentOfItsOwn(t *testing.T) {
	path := mainPath(t)
	long := sizedLine("msg_long", segmentBound+1)
	writeFile(t, path, userLine(1)+long+userLine(2))

	first := read(t, follow.File{Path: path})
	second := read(t, first.File)
	third := read(t, second.File)

	wantSegment(t, first, userLine(1))
	wantSegment(t, second, long)
	wantSegment(t, third, userLine(2))
	if !first.More || !second.More || third.More {
		t.Errorf("More = %v, %v, %v, want true, true, false", first.More, second.More, third.More)
	}
	wantCursor(t, third.File, int64(len(userLine(1)+long+userLine(2))), 3, "msg_long")
}

func TestReader_ARewriteReadInSeveralSegmentsSendsNoSeenMessageAgain(t *testing.T) {
	requireSessionSources(t)
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, assistantLine("msg_a", 0, "a")+assistantLine("msg_b", 0, "b"))
	a.register(path, "")
	a.answer = follow.Stored
	a.reader.Advance(path)
	a.handed = nil

	c, d := sizedLine("msg_c", 5<<20), sizedLine("msg_d", 5<<20)
	e := assistantLine("msg_e", 0, "e")
	replaceFile(t, path, c+d+assistantLine("msg_a", 0, "changed")+assistantLine("msg_b", 0, "changed")+e)
	a.reader.Advance(path)

	var got []string
	for _, res := range a.handed {
		got = append(got, segmentLines(t, res)...)
	}
	if want := []string{c, d, e}; strings.Join(got, "") != strings.Join(want, "") {
		t.Errorf("lines sent after the rewrite = %d lines, want msg_c, msg_d, msg_e and no line of a message sent before", len(got))
	}
	f := a.entry(path)
	if len(f.BeforeRewrite) != 0 {
		t.Errorf("ids kept for the rewrite pass = %q, want none once the pass reached the end", f.BeforeRewrite)
	}
	if f.NextSegment != 3 {
		t.Errorf("next segment = %d, want 3", f.NextSegment)
	}
}

func TestRead_LinesBeforeAMoveIntoConsentStayUnsentPastTheBound(t *testing.T) {
	path := mainPath(t)
	outside := sizedLine("msg_out1", 5<<20) + sizedLine("msg_out2", 5<<20)
	after := userLine(2) + assistantLine("msg_in", 0, "i")
	writeFile(t, path, outside+relocatedLine(root)+after)

	res := read(t, follow.File{Path: path})

	wantSegment(t, res, userLine(2), assistantLine("msg_in", 0, "i"))
	if res.More {
		t.Error("More = true, want the file read to its end")
	}
	wantCursor(t, res.File, int64(len(outside+relocatedLine(root)+after)), 1, "msg_in")
}
