package batch_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/mediablock"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

const fakeDBPassword = "hunter2hunter"

func TestBuild_RecordsThatShareHistoryPackAsEachRecordDoesAlone(t *testing.T) {
	in := sharedHistoryRawcalls(t, 12, 2<<10)
	whole, refused, err := batch.Build("batch-whole", buildTime, "test", in, batch.Run{}, mediablock.Policy{})
	if err != nil || len(refused) > 0 {
		t.Fatalf("Build: err=%v refused=%v", err, refused)
	}
	var alone []byte
	for _, e := range whole.Packed {
		one, refused, err := batch.Build("batch-one", buildTime, "test", spool.Entries{e}, batch.Run{}, mediablock.Policy{})
		if err != nil || len(refused) > 0 {
			t.Fatalf("Build of %s alone: err=%v refused=%v", e.ID, err, refused)
		}
		alone = append(alone, decompress(t, one.Records.Bytes())...)
	}
	got := decompress(t, whole.Records.Bytes())
	if !bytes.Equal(got, alone) {
		t.Fatalf("records packed together differ from the same records packed alone (%d vs %d bytes)", len(got), len(alone))
	}
	for _, secret := range []string{fakeSecret, fakeDBPassword, "dev@example.com", "pwd123"} {
		if bytes.Contains(got, []byte(secret)) {
			t.Errorf("%q went out unmasked", secret)
		}
	}
}

// BenchmarkBuild_RawcallsThatShareHistory measures a batch of the
// rawcalls of one session: each request repeats the system prompt, the
// tools and every earlier message, as a recorded conversation does.
func BenchmarkBuild_RawcallsThatShareHistory(b *testing.B) {
	in := sharedHistoryRawcalls(b, 20, 4<<10)
	var size int64
	for _, e := range in {
		size += int64(len(e.Raw))
	}
	b.SetBytes(size)
	b.ReportAllocs()
	for b.Loop() {
		if _, refused, err := batch.Build("batch-bench", buildTime, "test", in, batch.Run{}, mediablock.Policy{}); err != nil || len(refused) > 0 {
			b.Fatalf("Build: err=%v refused=%v", err, refused)
		}
	}
}

// sharedHistoryRawcalls records n calls of one session. Call k sends the
// same system prompt and tools and messages 0..k, each message about
// messageBytes of prose; some messages carry a credential, an email
// address, or a password under a password key and again as an argument.
func sharedHistoryRawcalls(tb testing.TB, n, messageBytes int) spool.Entries {
	tb.Helper()
	random := rand.New(rand.NewChaCha8([32]byte{1}))
	system := benchProse(random, 8*messageBytes)
	tools := benchProse(random, 4*messageBytes)
	var messages []any
	var entries spool.Entries
	for k := range n {
		messages = append(messages, historyMessage(random, k, messageBytes))
		body, err := json.Marshal(map[string]any{
			"model":    "claude-test",
			"system":   []any{map[string]any{"type": "text", "text": system}},
			"tools":    []any{map[string]any{"name": "Bash", "description": tools}},
			"metadata": map[string]any{"user_id": "history-session"},
			"messages": messages,
		})
		if err != nil {
			tb.Fatal(err)
		}
		id := fmt.Sprintf("req_history_%03d", k)
		response := `{"id":"msg_` + id + `","type":"message","content":[{"type":"text","text":"` + benchProse(random, 200) + `"}]}`
		entries = append(entries, storedRawcall(tb, id, "history-session", "hash-p1", string(body), response, buildTime.Add(time.Duration(k)*time.Second)))
	}
	return entries
}

func historyMessage(random *rand.Rand, k, messageBytes int) any {
	text := benchProse(random, messageBytes)
	switch k % 4 {
	case 0:
		return map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "text",
			"text": text + " Use api_key=" + fakeSecret + " and write to dev@example.com.",
		}}}
	case 1:
		return map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type":  "tool_use",
			"id":    fmt.Sprintf("toolu_%06d", k),
			"name":  "Bash",
			"input": map[string]any{"command": "psql postgres://app:pwd123@db.example.com:5432/app", "db_password": fakeDBPassword, "args": []any{"--password", fakeDBPassword}},
		}}}
	case 2:
		document, _ := json.Marshal(map[string]any{
			"database": map[string]any{"host": "db.internal.example.com", "user": "app", "password": fakeDBPassword},
			"notes":    text,
		})
		return map[string]any{"role": "user", "content": []any{map[string]any{
			"type":        "tool_result",
			"tool_use_id": fmt.Sprintf("toolu_%06d", k-1),
			"content":     string(document),
		}}}
	}
	return map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}
}

var benchWords = strings.Fields(`the agent read the file and ran the tests then wrote a short summary
of the diff token key secret auth api config build cache session request
handler parser index module router store update value error result`)

func benchProse(random *rand.Rand, size int) string {
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(benchWords[random.IntN(len(benchWords))])
		b.WriteByte(' ')
	}
	return b.String()
}
