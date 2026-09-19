package envelope_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// architectureMarkdownPath is the public description of what this
// binary writes. A reader checks it against the binary, so the version
// it states is pinned to the one the binary stamps.
const architectureMarkdownPath = "../../ARCHITECTURE.md"

var statedSchemaVersion = regexp.MustCompile(`schema_version ([0-9]+)`)

func TestArchitectureStatesTheSchemaVersionThisClientWrites(t *testing.T) {
	doc, err := os.ReadFile(architectureMarkdownPath)
	if err != nil {
		t.Fatal(err)
	}
	stated := statedSchemaVersion.FindAllStringSubmatch(string(doc), -1)
	if len(stated) == 0 {
		t.Fatalf("%s no longer states a schema_version", architectureMarkdownPath)
	}
	for _, m := range stated {
		if m[1] != envelope.SchemaVersion {
			t.Errorf("%s says schema_version %s; this client writes %s", architectureMarkdownPath, m[1], envelope.SchemaVersion)
		}
	}
}
