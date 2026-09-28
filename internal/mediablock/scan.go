package mediablock

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
)

// maxDepth bounds how deep the walk follows nested values. A record
// nested deeper is left as it is: encoding/json refuses the same depth,
// so no record this client stores reaches it.
const maxDepth = 10000

// comparedNames are all the names a key or a type is compared with.
// decodeName returns no other name, so a name compared elsewhere that is
// missing here never matches.
var comparedNames = [...]string{"type", "source", "data", "file", "base64", "toolUseResult", "image", "document", "pdf", "sha256", "bytes", "width", "height"}

// maxNameToken bounds the string tokens decoded for comparison with a
// name: the longest compared name written entirely as \u escapes, with
// its quotes. A longer token spells none of them.
var maxNameToken = len(`""`) + len(`\u0000`)*len(slices.MaxFunc(comparedNames[:], func(a, b string) int { return cmp.Compare(len(a), len(b)) }))

// findBlocks returns the blocks data holds, in the order they appear.
// data is one JSON value per line; a stored rawcall is one line. A line
// that is not exactly one JSON value holds no block: the service reads
// the same lines one at a time, and could not put back a reference
// written into a line it cannot read.
func findBlocks(data []byte) []block {
	w := walker{data: data}
	for start := 0; start < len(data); {
		end := len(data)
		if nl := bytes.IndexByte(data[start:], '\n'); nl >= 0 {
			end = start + nl
		}
		w.line(start, end)
		start = end + 1
	}
	slices.SortFunc(w.blocks, func(a, b block) int { return cmp.Compare(a.dataValue.start, b.dataValue.start) })
	return w.blocks
}

type walker struct {
	data   []byte
	blocks []block
}

// line adds the blocks of the line data[start:end], if it is one JSON
// value. The walk itself accepts more than JSON does, so it only locates
// the blocks in a line json.Valid has already accepted.
func (w *walker) line(start, end int) {
	if !json.Valid(w.data[start:end]) {
		return
	}
	found := len(w.blocks)
	if valueEnd, _, ok := w.value(skipSpace(w.data, start), 0); !ok || skipSpace(w.data[:end], valueEnd) != end {
		w.blocks = w.blocks[:found]
	}
}

// members is what an object says about the few keys a block is read
// from. Each count is how many times the key appeared: an object that
// names one of them twice is not read as a block, because which copy a
// reader takes is the reader's choice.
type members struct {
	typeCount, sourceCount, dataCount int
	typeValue                         span
	dataKey, dataValue                span
	// source describes the object under "source", when that value is an
	// object.
	source *members
	// The members a Read result is read from: its "file" object, and
	// the "base64" member in that.
	fileCount, base64Count int
	file                   *members
	base64Key, base64Value span
	// toolUseResult describes the object under "toolUseResult", when
	// that value is an object.
	toolUseResultCount int
	toolUseResult      *members
	// count is how many members the object has read so far, and lastEnd
	// where the value of the last one ended.
	count, lastEnd int
	// named are the members, in order, whose key a placeholder writes
	// beside the digest.
	named []namedMember
}

// namedMember is one member whose key a placeholder writes. index is
// its position among the object's members; prevEnd is where the value
// of the member before it ends, and next where the key of the member
// after it starts, each -1 when there is none.
type namedMember struct {
	name                      string
	index, keyStart, valueEnd int
	prevEnd, next             int
}

// value reads the value that starts at data[i] and returns where it
// ends. For an object it also returns what the object's members say.
func (w *walker) value(i, depth int) (int, *members, bool) {
	if i >= len(w.data) || depth > maxDepth {
		return 0, nil, false
	}
	switch c := w.data[i]; {
	case c == '{':
		return w.object(i, depth+1)
	case c == '[':
		end, ok := w.array(i, depth+1)
		return end, nil, ok
	case c == '"':
		end, ok := stringEnd(w.data, i)
		return end, nil, ok
	default:
		end := i
		for end < len(w.data) && !isDelimiter(w.data[end]) {
			end++
		}
		return end, nil, end > i
	}
}

func (w *walker) array(i, depth int) (int, bool) {
	i = skipSpace(w.data, i+1)
	if i < len(w.data) && w.data[i] == ']' {
		return i + 1, true
	}
	for i < len(w.data) {
		end, _, ok := w.value(i, depth)
		if !ok {
			return 0, false
		}
		i = skipSpace(w.data, end)
		if i >= len(w.data) {
			return 0, false
		}
		switch w.data[i] {
		case ',':
			i = skipSpace(w.data, i+1)
		case ']':
			return i + 1, true
		default:
			return 0, false
		}
	}
	return 0, false
}

func (w *walker) object(i, depth int) (int, *members, bool) {
	var m members
	i = skipSpace(w.data, i+1)
	if i < len(w.data) && w.data[i] == '}' {
		return i + 1, &m, true
	}
	for i < len(w.data) {
		if w.data[i] != '"' {
			return 0, nil, false
		}
		keyEnd, ok := stringEnd(w.data, i)
		if !ok {
			return 0, nil, false
		}
		key := span{i, keyEnd}
		i = skipSpace(w.data, keyEnd)
		if i >= len(w.data) || w.data[i] != ':' {
			return 0, nil, false
		}
		i = skipSpace(w.data, i+1)
		valueStart := i
		end, child, ok := w.value(i, depth)
		if !ok {
			return 0, nil, false
		}
		m.note(w.data, key, span{valueStart, end}, child)
		i = skipSpace(w.data, end)
		if i >= len(w.data) {
			return 0, nil, false
		}
		switch w.data[i] {
		case ',':
			i = skipSpace(w.data, i+1)
		case '}':
			if b, ok := m.block(w.data); ok {
				w.blocks = append(w.blocks, b)
			}
			// A Read result is read only where a session line keeps it:
			// under toolUseResult at the root of the line.
			if depth == 1 && m.toolUseResultCount == 1 && m.toolUseResult != nil {
				if b, ok := m.toolUseResult.readResult(w.data); ok {
					w.blocks = append(w.blocks, b)
				}
			}
			return i + 1, &m, true
		default:
			return 0, nil, false
		}
	}
	return 0, nil, false
}

// note records one member if its key is one a block is read from, or
// one a placeholder writes.
func (m *members) note(data []byte, key, value span, child *members) {
	index, prevEnd := m.count, -1
	if index > 0 {
		prevEnd = m.lastEnd
	}
	m.count, m.lastEnd = index+1, value.end
	if n := len(m.named); n > 0 && m.named[n-1].index == index-1 {
		m.named[n-1].next = key.start
	}
	name, ok := decodeName(data[key.start:key.end])
	if !ok {
		return
	}
	switch name {
	case "sha256", "bytes", "width", "height":
		m.named = append(m.named, namedMember{name: name, index: index, keyStart: key.start, valueEnd: value.end, prevEnd: prevEnd, next: -1})
	case "type":
		m.typeCount++
		m.typeValue = value
	case "source":
		m.sourceCount++
		m.source = child
	case "data":
		m.dataCount++
		m.dataKey, m.dataValue = key, value
	case "file":
		m.fileCount++
		m.file = child
	case "base64":
		m.base64Count++
		m.base64Key, m.base64Value = key, value
	case "toolUseResult":
		m.toolUseResultCount++
		m.toolUseResult = child
	}
}

// names reports whether the object has a member called name, among
// those a placeholder writes.
func (m *members) names(name string) bool {
	return slices.ContainsFunc(m.named, func(n namedMember) bool { return n.name == name })
}

// displaced returns the byte ranges that remove every member a
// placeholder writes, with the separators around them, so that what is
// left is still one object. The member a block's content is read from
// is never among them, so at least one member stays.
func (m *members) displaced() []span {
	var out []span
	for i := 0; i < len(m.named); {
		j := i
		for j+1 < len(m.named) && m.named[j+1].index == m.named[j].index+1 {
			j++
		}
		if m.named[j].next >= 0 {
			out = append(out, span{m.named[i].keyStart, m.named[j].next})
		} else {
			out = append(out, span{m.named[i].prevEnd, m.named[j].valueEnd})
		}
		i = j + 1
	}
	return out
}

// block reads the object as an image or document block, if it is one.
func (m *members) block(data []byte) (block, bool) {
	if m.typeCount != 1 || m.sourceCount != 1 || m.source == nil {
		return block{}, false
	}
	switch name, _ := decodeName(tokenAt(data, m.typeValue)); name {
	case "image", "document":
	default:
		return block{}, false
	}
	src := m.source
	if src.typeCount != 1 || src.dataCount != 1 {
		return block{}, false
	}
	if name, _ := decodeName(tokenAt(data, src.typeValue)); name != "base64" {
		return block{}, false
	}
	b := block{sourceType: src.typeValue, dataKey: src.dataKey, dataValue: src.dataValue, displaced: src.displaced()}
	// A source that already has a sha256 member would name it twice as a
	// reference; one reader then takes the digest and drops the member,
	// another takes the member and misses the reference.
	exact := !hasEscape(tokenAt(data, src.typeValue)) && !hasEscape(tokenAt(data, src.dataKey)) && !src.names("sha256")
	return b.withPayload(data, exact)
}

// readResult reads the object as the result Claude Code's Read tool
// keeps on a session line for an image or a PDF — a "file" object whose
// "base64" member is the content — if it is one. It carries the same
// content as the image or document block the call sent, so it is
// omitted with it; it is never referred to.
func (m *members) readResult(data []byte) (block, bool) {
	if m.typeCount != 1 || m.sourceCount != 0 || m.fileCount != 1 || m.file == nil {
		return block{}, false
	}
	switch name, _ := decodeName(tokenAt(data, m.typeValue)); name {
	case "image", "pdf":
	default:
		return block{}, false
	}
	if m.file.base64Count != 1 {
		return block{}, false
	}
	b := block{dataKey: m.file.base64Key, dataValue: m.file.base64Value, readResult: true, displaced: m.file.displaced()}
	return b.withPayload(data, false)
}

// withPayload completes a block whose content is the string at
// dataValue. A value that is not a string, or an empty one, carries no
// content, and the object is not a block. exact says the other tokens a
// reference replaces hold no escape.
func (b block) withPayload(data []byte, exact bool) (block, bool) {
	token := tokenAt(data, b.dataValue)
	if len(token) <= 2 || token[0] != '"' {
		return block{}, false
	}
	b.exact = exact && !hasEscape(token)
	if !hasEscape(token) {
		b.payload = token[1 : len(token)-1]
		return b, true
	}
	var payload string
	if err := json.Unmarshal(token, &payload); err != nil || payload == "" {
		return block{}, false
	}
	b.payload = []byte(payload)
	return b, true
}

func tokenAt(data []byte, s span) []byte { return data[s.start:s.end] }

// decodeName returns the compared name a string token spells, if it
// spells one.
func decodeName(token []byte) (string, bool) {
	if len(token) < 2 || token[0] != '"' || len(token) > maxNameToken {
		return "", false
	}
	text := token[1 : len(token)-1]
	if hasEscape(token) {
		var name string
		if err := json.Unmarshal(token, &name); err != nil {
			return "", false
		}
		text = []byte(name)
	}
	i := slices.IndexFunc(comparedNames[:], func(name string) bool { return string(text) == name })
	if i < 0 {
		return "", false
	}
	return comparedNames[i], true
}

func hasEscape(token []byte) bool { return bytes.IndexByte(token, '\\') >= 0 }

// stringEnd returns the index just past the closing quote of the string
// token that starts at data[i].
func stringEnd(data []byte, i int) (int, bool) {
	for j := i + 1; j < len(data); j++ {
		switch data[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return 0, false
}

func skipSpace(data []byte, i int) int {
	for i < len(data) && isSpace(data[i]) {
		i++
	}
	return i
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isDelimiter(c byte) bool {
	return isSpace(c) || c == ',' || c == ':' || c == '}' || c == ']' || c == '{' || c == '[' || c == '"'
}
