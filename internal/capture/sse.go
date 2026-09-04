package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// assemblyRulesVersion identifies the reassembly rules recorded in each
// envelope, so degraded records can be re-run under newer rules later.
const assemblyRulesVersion = "1"

// Assemble parses a complete Anthropic event stream and returns the
// equivalent non-streaming message object. Numbers are preserved as
// their source literals and streamed fragments (text, thinking,
// signatures, tool input) are concatenated verbatim; observed values
// are never rewritten. Any deviation — an unknown event or delta type,
// malformed data, a truncated stream — returns an error so the caller
// degrades to storing the raw stream: assembly never guesses.
func Assemble(raw []byte) (json.RawMessage, error) {
	events, err := parseEvents(raw)
	if err != nil {
		return nil, err
	}

	var message map[string]any
	var content []any
	pendingInput := map[int]*strings.Builder{}
	sawStop := false

	for _, ev := range events {
		if sawStop {
			return nil, fmt.Errorf("capture: event %q after message_stop", ev.name)
		}
		data, err := decodeObject(ev.data)
		if err != nil {
			return nil, fmt.Errorf("capture: event %q data: %w", ev.name, err)
		}
		if ev.name != "message_start" && message == nil {
			return nil, fmt.Errorf("capture: event %q before message_start", ev.name)
		}

		switch ev.name {
		case "message_start":
			if message != nil {
				return nil, fmt.Errorf("capture: duplicate message_start")
			}
			m, ok := data["message"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("capture: message_start without message object")
			}
			message = m
			if existing, ok := m["content"].([]any); ok {
				content = existing
			}

		case "content_block_start":
			idx, err := eventIndex(data)
			if err != nil {
				return nil, err
			}
			block, ok := data["content_block"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("capture: content_block_start without content_block object")
			}
			if idx != len(content) {
				return nil, fmt.Errorf("capture: content_block_start index %d, expected %d", idx, len(content))
			}
			content = append(content, block)

		case "content_block_delta":
			idx, err := eventIndex(data)
			if err != nil {
				return nil, err
			}
			block, err := blockAt(content, idx)
			if err != nil {
				return nil, err
			}
			if err := applyDelta(block, data, pendingInput, idx); err != nil {
				return nil, err
			}

		case "content_block_stop":
			idx, err := eventIndex(data)
			if err != nil {
				return nil, err
			}
			block, err := blockAt(content, idx)
			if err != nil {
				return nil, err
			}
			if pending, ok := pendingInput[idx]; ok {
				delete(pendingInput, idx)
				// Fragments that concatenate to nothing are the absence of a
				// fragment stream, not malformed JSON. Every tool input opens
				// with an input_json_delta carrying "", and for a tool with no
				// parameters the model emits no input tokens, so that empty
				// delta is the whole of it. Parsing it anyway failed the whole
				// assembly, which degraded the exchange to raw stream text —
				// and a degraded record is one JSON string, so the entropy
				// layer then masked the thinking signatures and ids the record
				// exists to preserve verbatim, with no way to recover them by
				// reassembling later. content_block_start already carried this
				// block's own input; leaving it is the observed truth.
				// 2026-08-26.
				if fragments := strings.TrimSpace(pending.String()); fragments != "" {
					input, err := decodeValue([]byte(fragments))
					if err != nil {
						return nil, fmt.Errorf("capture: accumulated tool input: %w", err)
					}
					block["input"] = input
				}
			}

		case "message_delta":
			if delta, ok := data["delta"].(map[string]any); ok {
				for k, v := range delta {
					message[k] = v
				}
			}
			if usage, ok := data["usage"].(map[string]any); ok {
				if existing, ok := message["usage"].(map[string]any); ok {
					for k, v := range usage {
						existing[k] = v
					}
				} else {
					message["usage"] = usage
				}
			}

		case "message_stop":
			sawStop = true

		case "ping":

		default:
			return nil, fmt.Errorf("capture: unknown event %q", ev.name)
		}
	}

	if message == nil {
		return nil, fmt.Errorf("capture: stream has no message_start")
	}
	if !sawStop {
		return nil, fmt.Errorf("capture: stream ended without message_stop")
	}
	if len(pendingInput) != 0 {
		return nil, fmt.Errorf("capture: tool input fragments left unclosed")
	}
	message["content"] = content
	return json.Marshal(message)
}

func applyDelta(block, data map[string]any, pendingInput map[int]*strings.Builder, idx int) error {
	delta, ok := data["delta"].(map[string]any)
	if !ok {
		return fmt.Errorf("capture: content_block_delta without delta object")
	}
	kind, _ := delta["type"].(string)
	switch kind {
	case "text_delta":
		return appendString(block, delta, "text")
	case "thinking_delta":
		return appendString(block, delta, "thinking")
	case "signature_delta":
		return appendString(block, delta, "signature")
	case "input_json_delta":
		fragment, ok := delta["partial_json"].(string)
		if !ok {
			return fmt.Errorf("capture: input_json_delta without partial_json string")
		}
		if pendingInput[idx] == nil {
			pendingInput[idx] = &strings.Builder{}
		}
		pendingInput[idx].WriteString(fragment)
		return nil
	case "citations_delta":
		citation, ok := delta["citation"]
		if !ok {
			return fmt.Errorf("capture: citations_delta without citation")
		}
		existing, _ := block["citations"].([]any)
		block["citations"] = append(existing, citation)
		return nil
	default:
		return fmt.Errorf("capture: unknown delta type %q", kind)
	}
}

func appendString(block, delta map[string]any, field string) error {
	fragment, ok := delta[field].(string)
	if !ok {
		return fmt.Errorf("capture: %s_delta without %s string", field, field)
	}
	existing, _ := block[field].(string)
	block[field] = existing + fragment
	return nil
}

func blockAt(content []any, idx int) (map[string]any, error) {
	if idx < 0 || idx >= len(content) {
		return nil, fmt.Errorf("capture: delta for unstarted content block %d", idx)
	}
	block, ok := content[idx].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("capture: content block %d is not an object", idx)
	}
	return block, nil
}

func eventIndex(data map[string]any) (int, error) {
	n, ok := data["index"].(json.Number)
	if !ok {
		return 0, fmt.Errorf("capture: event without numeric index")
	}
	idx, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("capture: event index %q: %w", n, err)
	}
	return int(idx), nil
}

func decodeObject(data []byte) (map[string]any, error) {
	v, err := decodeValue(data)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("data is not an object")
	}
	return m, nil
}

// decodeValue parses JSON keeping numbers as source literals so
// reassembly never re-encodes them through a float.
func decodeValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

type event struct {
	name string
	data []byte
}

// parseEvents splits a server-sent event stream into named events. An
// event is only delivered once terminated by a blank line, so a stream
// cut mid-event fails here rather than yielding a half-parsed record.
func parseEvents(raw []byte) ([]event, error) {
	var events []event
	var name string
	var data []byte
	fieldsSeen, dataSeen := false, false

	flush := func() {
		if fieldsSeen {
			events = append(events, event{name: name, data: data})
		}
		name, data = "", nil
		fieldsSeen, dataSeen = false, false
	}

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
			fieldsSeen = true
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if dataSeen {
				data = append(data, '\n')
			}
			data = append(data, chunk...)
			fieldsSeen, dataSeen = true, true
		case strings.Contains(line, ":"):
			// A field this parser does not model. `id:` and `retry:` are
			// standard SSE and cost nothing to skip; so is any field the
			// upstream adds later, none of which we reassemble from.
			//
			// Until 2026-09-04 this was an error, which is the wrong
			// direction here. Failing the parse degrades the whole exchange
			// to raw stream text, and a degraded record is a single JSON
			// string, so the entropy layer then masks the thinking
			// signatures and ids this record exists to keep verbatim. One
			// spec-legal line upstream could start emitting at any time
			// would have cost observed truth on every stream. Skipping a
			// field we never read costs nothing. A line carrying no colon
			// at all stays an error: that is framing damage, not an
			// unmodelled field.
		default:
			return nil, fmt.Errorf("capture: unrecognized stream line %q", line)
		}
	}
	if fieldsSeen {
		return nil, fmt.Errorf("capture: stream ended mid-event")
	}
	return events, nil
}
