package mediablock_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/mediablock"
)

const (
	scope      = "proxy\x00session-1"
	signature  = "EqQBCkgIBBgCIkDunT5RmZFPqBWEcTbEK4DZWWL9zGnDx0M0vGRnHkV6wZQx"
	messageID  = "msg_01FixtureMessageId0001"
	pdfPayload = "JVBERi0xLjQKJWZha2UgZG9jdW1lbnQK" // "%PDF-1.4\n%fake document\n"
)

// tinyPNG is a constructed 3×2 image: a few dozen bytes of valid PNG,
// never a real screenshot.
func tinyPNG(t *testing.T, shade uint8) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 3, 2))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	img.Set(0, 0, color.Gray{Y: 255 - shade})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func imageBlock(payload string) string {
	return `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}`
}

func documentBlock(payload string) string {
	return `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + payload + `"},"title":"notes"}`
}

// request is a record holding blocks the way a recorded call does: a
// signed thinking block, then a tool result whose content is the blocks.
func request(blocks ...string) string {
	return `{"request":{"metadata":{"user_id":"session-1"},"messages":[` +
		`{"role":"assistant","id":"` + messageID + `","content":[{"type":"thinking","thinking":"","signature":"` + signature + `"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[` + strings.Join(blocks, ",") + `]}]}]}}`
}

func digestOf(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func sentNothing(string, string) bool { return false }

func sentOnly(payloads ...string) func(string, string) bool {
	return func(s, digest string) bool {
		for _, p := range payloads {
			if s == scope && digest == digestOf(p) {
				return true
			}
		}
		return false
	}
}

var referencePattern = regexp.MustCompile(`"type":"sha256_ref"(,"media_type":"[^"]*")?,"sha256":"([0-9a-f]{64})"`)

// restore puts every reference back the way the receiving side does:
// the three tokens a reference replaced are replaced again.
func restore(t *testing.T, data []byte, payloads ...string) []byte {
	t.Helper()
	byDigest := map[string]string{}
	for _, p := range payloads {
		byDigest[digestOf(p)] = p
	}
	return referencePattern.ReplaceAllFunc(data, func(m []byte) []byte {
		sub := referencePattern.FindSubmatch(m)
		payload, ok := byDigest[string(sub[2])]
		if !ok {
			t.Fatalf("a reference names a payload the test never sent: %s", sub[2])
		}
		return []byte(`"type":"base64"` + string(sub[1]) + `,"data":"` + payload + `"`)
	})
}

// sources lists the source object of every block the record carries,
// in order.
func sources(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if typ, _ := v["type"].(string); typ == "image" || typ == "document" {
				if src, ok := v["source"].(map[string]any); ok {
					out = append(out, src)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("a rewritten line is not JSON: %v", err)
		}
		walk(v)
	}
	return out
}

func TestRewrite_WithoutAReferenceSignalEveryCopyStaysInFull(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(request(imageBlock(a), imageBlock(a)))
	out := mediablock.NewPass(mediablock.Policy{}).Rewrite(scope, in)
	if !bytes.Equal(out.Data, in) {
		t.Fatalf("a pass with no reference signal changed the record:\n%s", out.Data)
	}
}

func TestRewrite_FirstCopyInARecordStaysAndLaterCopiesBecomeReferences(t *testing.T) {
	a, b := tinyPNG(t, 10), tinyPNG(t, 20)
	in := []byte(request(imageBlock(a), imageBlock(b), imageBlock(a), imageBlock(a)))
	out := mediablock.NewPass(mediablock.Policy{Sent: sentNothing}).Rewrite(scope, in)

	got := sources(t, out.Data)
	want := []string{"base64", "base64", mediablock.SourceReference, mediablock.SourceReference}
	for i, src := range got {
		if src["type"] != want[i] {
			t.Fatalf("block %d has source type %v, want %s", i, src["type"], want[i])
		}
	}
	if got[2]["sha256"] != digestOf(a) || got[2]["data"] != nil {
		t.Fatalf("a reference must carry the digest of the base64 text and no data: %v", got[2])
	}
	if got[2]["media_type"] != "image/png" {
		t.Fatalf("a reference keeps the media type as observed: %v", got[2])
	}
	if !bytes.Equal(restore(t, out.Data, a, b), in) {
		t.Fatalf("putting the references back does not give the record byte for byte:\n%s", restore(t, out.Data, a, b))
	}
}

func TestRewrite_APayloadTheServiceHoldsIsReferredToFromItsFirstCopy(t *testing.T) {
	a, b := tinyPNG(t, 10), tinyPNG(t, 20)
	in := []byte(request(imageBlock(a), imageBlock(b)))
	out := mediablock.NewPass(mediablock.Policy{Sent: sentOnly(a)}).Rewrite(scope, in)
	got := sources(t, out.Data)
	if got[0]["type"] != mediablock.SourceReference || got[1]["type"] != "base64" {
		t.Fatalf("source types = %v, %v; want a reference to the held payload and the other in full", got[0]["type"], got[1]["type"])
	}
	if !bytes.Equal(restore(t, out.Data, a), in) {
		t.Fatal("putting the reference back does not give the record byte for byte")
	}
}

func TestRewrite_AnEmptyScopeNeverRefers(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(request(imageBlock(a), imageBlock(a)))
	out := mediablock.NewPass(mediablock.Policy{Sent: sentOnly(a)}).Rewrite("", in)
	if !bytes.Equal(out.Data, in) {
		t.Fatalf("a record naming no session was rewritten:\n%s", out.Data)
	}
}

func TestRewrite_OmitReplacesEveryCopyWithAPlaceholder(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(request(imageBlock(a), imageBlock(a), documentBlock(pdfPayload)))
	pass := mediablock.NewPass(mediablock.Policy{Omit: true, Sent: sentOnly(a)})
	out := pass.Rewrite(scope, in)
	pass.Keep(out)

	if bytes.Contains(out.Data, []byte(a)) || bytes.Contains(out.Data, []byte(pdfPayload)) {
		t.Fatal("a payload is still in the record with upload turned off")
	}
	decoded, _ := base64.StdEncoding.DecodeString(a)
	got := sources(t, out.Data)
	for i, src := range got[:2] {
		want := map[string]any{
			"type": mediablock.SourceOmitted, "media_type": "image/png", "sha256": digestOf(a),
			"bytes": float64(len(decoded)), "width": float64(3), "height": float64(2),
		}
		for k, v := range want {
			if src[k] != v {
				t.Fatalf("image placeholder %d: %s = %v, want %v (%v)", i, k, src[k], v, src)
			}
		}
	}
	doc := got[2]
	if doc["type"] != mediablock.SourceOmitted || doc["bytes"] != float64(24) || doc["width"] != nil {
		t.Fatalf("a document placeholder states its size and no dimensions: %v", doc)
	}
	if !strings.Contains(string(out.Data), `"title":"notes"`) {
		t.Fatal("the members of a block outside its source must stay")
	}
	if len(pass.Originals()) != 0 {
		t.Fatalf("a placeholder is not a payload carried in full: %v", pass.Originals())
	}
}

func TestRewrite_APlaceholderStatesNoSizeItCannotRead(t *testing.T) {
	in := []byte(request(imageBlock("bm90IGFuIGltYWdl"), imageBlock("%%%not-base64%%%")))
	out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, in)
	got := sources(t, out.Data)
	if got[0]["bytes"] != float64(12) || got[0]["width"] != nil {
		t.Fatalf("bytes that are no image state their size only: %v", got[0])
	}
	if got[1]["bytes"] != nil || got[1]["sha256"] != digestOf("%%%not-base64%%%") {
		t.Fatalf("text that is not base64 states only its digest: %v", got[1])
	}
}

func TestRewrite_LeavesEveryOtherShapeAlone(t *testing.T) {
	a := tinyPNG(t, 10)
	cases := map[string]string{
		"type named like an image":   `{"type":"image_metadata","source":{"type":"base64","media_type":"image/png","data":"` + a + `"}}`,
		"type in another case":       `{"type":"Image","source":{"type":"base64","media_type":"image/png","data":"` + a + `"}}`,
		"source by url":              `{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}`,
		"source named like base64":   `{"type":"image","source":{"type":"base64url","data":"` + a + `"}}`,
		"document as plain text":     `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"` + a + `"}}`,
		"data that is not a string":  `{"type":"image","source":{"type":"base64","data":null}}`,
		"source that is not object":  `{"type":"image","source":"` + a + `"}`,
		"type named twice":           `{"type":"image","type":"image","source":{"type":"base64","data":"` + a + `"}}`,
		"data named twice":           `{"type":"image","source":{"type":"base64","data":"` + a + `","data":"` + a + `"}}`,
		"payload beside a tool name": `{"type":"image","file":{"base64":"` + a + `","type":"image/png"}}`,
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			in := []byte(request(block, block))
			for _, policy := range []mediablock.Policy{{Sent: sentOnly(a)}, {Omit: true}} {
				if out := mediablock.NewPass(policy).Rewrite(scope, in); !bytes.Equal(out.Data, in) {
					t.Fatalf("rewritten under %+v:\n%s", policy.Omit, out.Data)
				}
			}
		})
	}
}

func TestRewrite_SignaturesAndMessageIDsStayByteForByte(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(request(imageBlock(a), imageBlock(a)))
	for _, policy := range []mediablock.Policy{{Sent: sentOnly(a)}, {Omit: true}} {
		out := mediablock.NewPass(policy).Rewrite(scope, in)
		for _, observed := range []string{`"signature":"` + signature + `"`, `"id":"` + messageID + `"`, `"type":"thinking","thinking":""`} {
			if !bytes.Contains(out.Data, []byte(observed)) {
				t.Fatalf("%s is not in the rewritten record byte for byte", observed)
			}
		}
		if bytes.Index(out.Data, []byte(signature)) > bytes.Index(out.Data, []byte(`"tool_result"`)) {
			t.Fatal("the blocks were reordered")
		}
	}
}

func TestRewrite_AnEscapedCopyIsNeverReferredToButIsStillOmitted(t *testing.T) {
	a := tinyPNG(t, 10)
	escaped := fmt.Sprintf(`\u%04x`, a[0]) + a[1:]
	in := []byte(request(imageBlock(a), imageBlock(escaped)))
	out := mediablock.NewPass(mediablock.Policy{Sent: sentOnly(a)}).Rewrite(scope, in)
	got := sources(t, out.Data)
	if got[0]["type"] != mediablock.SourceReference || got[1]["type"] != "base64" {
		t.Fatalf("source types = %v, %v; an escaped copy cannot be restored byte for byte, so it stays", got[0]["type"], got[1]["type"])
	}
	omitted := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, in)
	got = sources(t, omitted.Data)
	if got[1]["type"] != mediablock.SourceOmitted || got[1]["sha256"] != digestOf(a) {
		t.Fatalf("an escaped copy is omitted under the digest of its decoded text: %v", got[1])
	}
}

func TestRewrite_ALineThatIsNotJSONStaysAndTheOthersAreRewritten(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(request(imageBlock(a)) + "\n" + `{"cut":` + imageBlock(a) + "\n" + request(imageBlock(a)) + "\n")
	out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, in)
	lines := strings.Split(string(out.Data), "\n")
	if strings.Contains(lines[0], a) || strings.Contains(lines[2], a) {
		t.Fatal("a line that reads as JSON kept its payload")
	}
	if lines[1] != `{"cut":`+imageBlock(a) {
		t.Fatalf("a line that is not JSON was changed: %s", lines[1])
	}
}

func TestPass_ALaterRecordRefersOnlyToAKeptRecordOfItsOwnScope(t *testing.T) {
	a := tinyPNG(t, 10)
	record := []byte(request(imageBlock(a)))
	pass := mediablock.NewPass(mediablock.Policy{Sent: sentNothing})

	dropped := pass.Rewrite(scope, record)
	if !bytes.Equal(dropped.Data, record) {
		t.Fatal("a first copy was not sent in full")
	}
	if again := pass.Rewrite(scope, record); !bytes.Equal(again.Data, record) {
		t.Fatal("a record referred to a payload whose record was never kept")
	}
	pass.Keep(dropped)
	if other := pass.Rewrite("transcript\x00session-1", record); !bytes.Equal(other.Data, record) {
		t.Fatal("a record of another scope referred to the kept payload")
	}
	later := pass.Rewrite(scope, record)
	if got := sources(t, later.Data); got[0]["type"] != mediablock.SourceReference {
		t.Fatalf("a later record of the same scope did not refer to the kept payload: %v", got[0])
	}
	pass.Keep(later)
	originals := pass.Originals()
	if len(originals) != 1 || originals[0] != (mediablock.Original{Scope: scope, Digest: digestOf(a)}) {
		t.Fatalf("Originals = %v, want the one payload carried in full", originals)
	}
}

func TestPass_ARecordWithoutAScopeLendsNoPayload(t *testing.T) {
	a := tinyPNG(t, 10)
	pass := mediablock.NewPass(mediablock.Policy{Sent: sentNothing})
	pass.Keep(pass.Rewrite("", []byte(request(imageBlock(a)))))
	if len(pass.Originals()) != 0 {
		t.Fatalf("Originals = %v; a record naming no session can never be referred to", pass.Originals())
	}
}

func TestRewrite_ARecordWithoutBlocksIsReturnedAsItIs(t *testing.T) {
	for _, in := range []string{`{"messages":[]}`, `{"text":"base64 is mentioned"}`, `{"text":"\u00e9"}`, ``, `[[[`} {
		out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, []byte(in))
		if string(out.Data) != in {
			t.Fatalf("Rewrite(%q) = %q", in, out.Data)
		}
	}
}

func TestRewrite_TakesAnEscapedNameAsTheNameItSpells(t *testing.T) {
	a := tinyPNG(t, 10)
	esc := func(c byte) string { return fmt.Sprintf(`\u%04x`, c) }
	block := `{"type":"im` + esc('a') + `ge","source":{"typ` + esc('e') + `":"base64","media_type":"image/png","d` + esc('a') + `ta":"` + a + `"}}`
	in := []byte(request(block, block))
	got := sources(t, mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, in).Data)
	if len(got) != 2 || got[0]["type"] != mediablock.SourceOmitted {
		t.Fatalf("an escaped spelling of a block was not omitted: %v", got)
	}
	out := mediablock.NewPass(mediablock.Policy{Sent: sentOnly(a)}).Rewrite(scope, in)
	if !bytes.Equal(out.Data, in) {
		t.Fatal("a block whose rewritten tokens hold an escape was referred to, which cannot be put back byte for byte")
	}
}

func TestRewrite_LeavesAValueThatDoesNotParseAsItIs(t *testing.T) {
	a := tinyPNG(t, 10)
	block := imageBlock(a)
	cases := []string{
		`{"a":[1,2}` + block,
		`{"a" 1,"b":` + block + `}`,
		`{"a":` + block + ` x}`,
		`{1:` + block + `}`,
		`{"a":` + block + `,"b":"unterminated`,
		`[` + block + ` 2]`,
		`[` + block + `,`,
		`{"a":` + block,
		`{"a":` + block + `,`,
		strings.Repeat("[", 10002) + block + strings.Repeat("]", 10002),
	}
	for _, in := range cases {
		out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, []byte(in))
		if string(out.Data) != in {
			t.Fatalf("a value that does not parse was rewritten: %.80s", in)
		}
	}
}

func TestRewrite_LeavesALineThatIsNotExactlyOneJSONValueAsItIs(t *testing.T) {
	a := tinyPNG(t, 10)
	block := imageBlock(a)
	cases := map[string]string{
		"text after the value":          block + ` trailing`,
		"a scalar that is not JSON":     `{"a":` + block + `,"x":nul}`,
		"an escape that is not JSON":    `{"a":` + block + `,"x":"bad\q"}`,
		"a control byte in a string":    `{"a":` + block + ",\"x\":\"tab\there\"}",
		"two values on one line":        block + ` ` + block,
		"one value across two lines":    `{"a":` + "\n" + block + `}`,
		"one document across two lines": "{\"messages\":\n[" + block + "]}",
	}
	policies := map[string]mediablock.Policy{
		"omit":  {Omit: true},
		"refer": {Sent: func(string, string) bool { return true }},
	}
	for name, line := range cases {
		for policyName, policy := range policies {
			t.Run(name+"/"+policyName, func(t *testing.T) {
				in := []byte(request(block) + "\n" + line + "\n")
				out := mediablock.NewPass(policy).Rewrite(scope, in)
				first, rest, _ := strings.Cut(string(out.Data), "\n")
				if strings.Contains(first, a) {
					t.Fatalf("the line that reads as JSON kept its payload:\n%s", first)
				}
				if rest != line+"\n" {
					t.Fatalf("a line that is not one JSON value was changed:\n got %s\nwant %s", rest, line)
				}
			})
		}
	}
}

// escaped spells a name as a JSON string written entirely in \u
// escapes.
func escaped(name string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range []byte(name) {
		fmt.Fprintf(&b, `\u%04x`, c)
	}
	b.WriteByte('"')
	return b.String()
}

func TestRewrite_OmitsEveryShapeWhoseNamesAreSpelledEntirelyInEscapes(t *testing.T) {
	a := tinyPNG(t, 10)
	block := `{` + escaped("type") + `:` + escaped("document") + `,` + escaped("source") + `:{` +
		escaped("type") + `:` + escaped("base64") + `,` + escaped("data") + `:"` + pdfPayload + `"}}`
	readResult := `{` + escaped("type") + `:` + escaped("image") + `,` + escaped("file") + `:{` +
		escaped("base64") + `:"` + a + `"}}`
	in := []byte(`{"message":{"content":[` + block + `]},` + escaped("toolUseResult") + `:` + readResult + `}` + "\n")
	out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite("transcript\x00session-1", in)
	if bytes.Contains(out.Data, []byte(pdfPayload)) {
		t.Fatalf("a block whose names are all escapes left with upload turned off:\n%s", out.Data)
	}
	if bytes.Contains(out.Data, []byte(a)) {
		t.Fatalf("a Read result under an escaped toolUseResult key left with upload turned off:\n%s", out.Data)
	}
}

// readLine is a session line the way Claude Code writes the result of
// a Read of an image: the block the call sent, and the tool's own
// result holding the same content again.
func readLine(resultType, mediaType, payload string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[` +
		imageBlock(payload) + `]}]},"toolUseResult":{"type":"` + resultType + `","file":{"base64":"` + payload +
		`","type":"` + mediaType + `","originalSize":70}}}` + "\n"
}

func TestRewrite_OmitsTheContentAReadResultKeepsOnASessionLine(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(readLine("image", "image/png", a) + `{"toolUseResult":{"type":"pdf","file":{"filePath":"/tmp/notes.pdf","base64":"` + pdfPayload + `","originalSize":24}}}` + "\n")
	out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite("transcript\x00session-1", in)
	if bytes.Contains(out.Data, []byte(a)) || bytes.Contains(out.Data, []byte(pdfPayload)) {
		t.Fatal("content a Read result keeps left the machine with upload turned off")
	}
	lines := strings.Split(string(out.Data), "\n")
	decoded, _ := base64.StdEncoding.DecodeString(a)
	placeholder := `"sha256":"` + digestOf(a) + `","bytes":` + fmt.Sprint(len(decoded)) + `,"width":3,"height":2`
	if strings.Count(lines[0], placeholder) != 2 {
		t.Fatalf("the block and the Read result do not state one placeholder:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], `"toolUseResult":{"type":"image","file":{`+placeholder+`,"type":"image/png","originalSize":70}}`) {
		t.Fatalf("the Read result placeholder changed more than its content:\n%s", lines[0])
	}
	if !strings.Contains(lines[1], `"file":{"filePath":"/tmp/notes.pdf","sha256":"`+digestOf(pdfPayload)+`","bytes":24,"originalSize":24}`) {
		t.Fatalf("a PDF Read result states its digest and size only:\n%s", lines[1])
	}
}

func TestRewrite_NeverRefersToTheContentAReadResultKeeps(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(readLine("image", "image/png", a))
	pass := mediablock.NewPass(mediablock.Policy{Sent: sentOnly(a)})
	out := pass.Rewrite(scope, in)
	pass.Keep(out)
	if !strings.Contains(string(out.Data), `"file":{"base64":"`+a+`"`) {
		t.Fatalf("a Read result was referred to:\n%s", out.Data)
	}
	if got := sources(t, out.Data); got[0]["type"] != mediablock.SourceReference {
		t.Fatalf("the block beside it was not: %v", got[0])
	}
}

func TestRewrite_LeavesEveryOtherReadResultAlone(t *testing.T) {
	a := tinyPNG(t, 10)
	cases := map[string]string{
		"not at the root of the line": `{"message":{"toolUseResult":{"type":"image","file":{"base64":"` + a + `"}}}}`,
		"a result of another type":    `{"toolUseResult":{"type":"text","file":{"base64":"` + a + `"}}}`,
		"content already emptied":     `{"toolUseResult":{"type":"image","file":{"base64":"","type":"image/png"}}}`,
		"content that is no string":   `{"toolUseResult":{"type":"image","file":{"base64":null}}}`,
		"content named twice":         `{"toolUseResult":{"type":"image","file":{"base64":"` + a + `","base64":"` + a + `"}}}`,
		"a notebook":                  `{"toolUseResult":{"type":"notebook","file":{"cells":[{"outputs":[{"image":{"image_data":"` + a + `"}}]}]}}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			in := []byte(line + "\n")
			if out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, in); !bytes.Equal(out.Data, in) {
				t.Fatalf("rewritten:\n%s", out.Data)
			}
		})
	}
}

// assertNoDuplicateKeys fails when a line of data is not JSON or names
// a key twice in one object: json.Valid accepts both, and a reader then
// picks one copy on its own.
func assertNoDuplicateKeys(t *testing.T, data []byte) {
	t.Helper()
	var value func(dec *json.Decoder) error
	value = func(dec *json.Decoder) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				if seen[key.(string)] {
					return fmt.Errorf("key %q named twice in one object", key)
				}
				seen[key.(string)] = true
				if err := value(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for dec.More() {
				if err := value(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("a rewritten line is not JSON:\n%s", line)
		}
		if err := value(json.NewDecoder(strings.NewReader(line))); err != nil {
			t.Fatalf("%v:\n%s", err, line)
		}
	}
}

func TestRewrite_ACopyWhoseSourceAlreadyNamesSHA256IsSentInFull(t *testing.T) {
	a := tinyPNG(t, 10)
	cases := map[string]string{
		"before the data": `{"type":"image","source":{"type":"base64","sha256":"x","data":"` + a + `"}}`,
		"after the data":  `{"type":"image","source":{"type":"base64","data":"` + a + `","sha256":"` + digestOf("other") + `"}}`,
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			in := []byte(request(imageBlock(a), block, imageBlock(a)))
			pass := mediablock.NewPass(mediablock.Policy{Sent: sentOnly(a)})
			out := pass.Rewrite(scope, in)
			assertNoDuplicateKeys(t, out.Data)
			if !strings.Contains(string(out.Data), block) {
				t.Fatalf("a copy whose source already names sha256 was not sent as it is:\n%s", out.Data)
			}
			if got := sources(t, out.Data); got[0]["type"] != mediablock.SourceReference || got[2]["type"] != mediablock.SourceReference {
				t.Fatalf("the copies beside it are still referred to: %v, %v", got[0]["type"], got[2]["type"])
			}
			pass.Keep(out)
			if got := pass.Originals(); len(got) != 1 || got[0].Digest != digestOf(a) {
				t.Fatalf("the copy sent in full is a payload the batch carries: %v", got)
			}
		})
	}
}

func TestRewrite_APlaceholderReplacesTheSourceMembersNamedLikeItsOwn(t *testing.T) {
	a := tinyPNG(t, 10)
	decoded, _ := base64.StdEncoding.DecodeString(a)
	nested := imageBlock(tinyPNG(t, 20))
	cases := map[string]string{
		"sha256 before the data":      `{"type":"base64","sha256":"x","data":"` + a + `","note":1}`,
		"sha256 after the data":       `{"type":"base64","data":"` + a + `","sha256":"` + digestOf("other") + `"}`,
		"bytes before the data":       `{"type":"base64","bytes":7,"data":"` + a + `"}`,
		"bytes after the data":        `{"type":"base64","data":"` + a + `","bytes":7,"note":1}`,
		"width after the data":        `{"type":"base64","data":"` + a + `","width":9}`,
		"height first":                `{"height":9,"type":"base64","data":"` + a + `"}`,
		"all of them around the data": `{"width":9,"height":9,"type":"base64","sha256":"x","data":"` + a + `","bytes":7,"note":1,"width":9,"height":9}`,
		"spaced out":                  `{ "type" : "base64" , "bytes" : 7 , "data" : "` + a + `" , "height" : 9 }`,
		"holding a block of its own":  `{"type":"base64","data":"` + a + `","width":` + nested + `}`,
		"named with an escape":        `{"type":"base64","data":"` + a + `",` + escaped("width") + `:9}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			in := []byte(request(`{"type":"image","source":` + source + `}`))
			out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, in)
			assertNoDuplicateKeys(t, out.Data)
			got := sources(t, out.Data)[0]
			want := map[string]any{
				"type": mediablock.SourceOmitted, "sha256": digestOf(a),
				"bytes": float64(len(decoded)), "width": float64(3), "height": float64(2),
			}
			for k, v := range want {
				if got[k] != v {
					t.Fatalf("%s = %v, want %v:\n%s", k, got[k], v, out.Data)
				}
			}
			if strings.Contains(source, `"note":1`) != strings.Contains(string(out.Data), `"note":1`) {
				t.Fatalf("a source member the placeholder does not write was changed:\n%s", out.Data)
			}
		})
	}
}

func TestRewrite_APlaceholderLeavesNoSizeItDidNotReadFromTheSource(t *testing.T) {
	in := []byte(request(`{"type":"document","source":{"type":"base64","data":"%%%not-base64%%%","bytes":7,"width":9,"height":9}}`))
	out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite(scope, in)
	assertNoDuplicateKeys(t, out.Data)
	got := sources(t, out.Data)[0]
	if got["bytes"] != nil || got["width"] != nil || got["height"] != nil {
		t.Fatalf("a size the placeholder could not read was left in its place: %v", got)
	}
}

func TestRewrite_AReadResultPlaceholderReplacesTheFileMembersNamedLikeItsOwn(t *testing.T) {
	a := tinyPNG(t, 10)
	in := []byte(`{"toolUseResult":{"type":"image","file":{"sha256":"x","base64":"` + a + `","width":9,"type":"image/png"}}}` + "\n")
	out := mediablock.NewPass(mediablock.Policy{Omit: true}).Rewrite("transcript\x00session-1", in)
	assertNoDuplicateKeys(t, out.Data)
	decoded, _ := base64.StdEncoding.DecodeString(a)
	want := `{"toolUseResult":{"type":"image","file":{"sha256":"` + digestOf(a) + `","bytes":` + fmt.Sprint(len(decoded)) + `,"width":3,"height":2,"type":"image/png"}}}` + "\n"
	if string(out.Data) != want {
		t.Fatalf("got  %s\nwant %s", out.Data, want)
	}
}
