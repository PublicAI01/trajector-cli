package conformance

import (
	"os"
	"path/filepath"
)

// BlocksCase is one fixture of the rewrite a batch applies to image and
// document blocks: the records one batch packs, in order, each with the
// bytes this client must send for it.
type BlocksCase struct {
	// Name is the fixture's directory name, used in test output.
	Name string
	Meta BlocksMeta
	// Records are the records of the batch, in the order they are
	// packed.
	Records []BlocksRecord
}

// BlocksMeta is the fixture's own statement of what it covers and of
// the policy the batch is built under.
type BlocksMeta struct {
	Name        string `json:"name"`
	ContractRef string `json:"contract_ref"`
	// BlockRefs is true when the service said, in the acknowledgement
	// before this batch, that it puts references back. False means that
	// no reference is written.
	BlockRefs bool `json:"block_refs"`
	// Omit is true when the user turned off the upload of images and
	// documents.
	Omit bool `json:"omit"`
	// Sent lists the digests of the payloads that an acknowledged batch
	// carried in full before this one.
	Sent []string `json:"sent"`
	// Records names the files of each record.
	Records []BlocksRecordFiles `json:"records"`
}

// BlocksRecordFiles names the files of one record of a case.
type BlocksRecordFiles struct {
	// Path is the recording path that read the record: "proxy" or
	// "transcript". Each path has its own scope.
	Path   string `json:"path"`
	Input  string `json:"input"`
	Expect string `json:"expect"`
	// JSONDuplicateKeys marks a record that names a key twice in one
	// object. Its meaning after decoding depends on the decoder.
	JSONDuplicateKeys bool `json:"json_duplicate_keys"`
}

// BlocksRecord is one record of a case: the recording path, the record
// as it was stored, and the bytes the batch must carry for it.
type BlocksRecord struct {
	Path   string
	Input  []byte
	Expect []byte
}

// LoadBlocks reads every block case under dir, in name order. A fixture
// set without the blocks directory is an error: the caller found the
// fixtures, so a part of them that is missing is not the normal absence
// Find reports.
func LoadBlocks(dir string) ([]BlocksCase, error) {
	root := filepath.Join(dir, "blocks")
	names, err := caseDirs(root)
	if err != nil {
		return nil, err
	}
	var cases []BlocksCase
	for _, name := range names {
		base := filepath.Join(root, name)
		c := BlocksCase{Name: name}
		if err := readJSON(filepath.Join(base, caseFile), &c.Meta); err != nil {
			return nil, err
		}
		for _, files := range c.Meta.Records {
			input, err := os.ReadFile(filepath.Join(base, files.Input))
			if err != nil {
				return nil, err
			}
			expect, err := os.ReadFile(filepath.Join(base, files.Expect))
			if err != nil {
				return nil, err
			}
			c.Records = append(c.Records, BlocksRecord{Path: files.Path, Input: input, Expect: expect})
		}
		cases = append(cases, c)
	}
	return cases, nil
}
