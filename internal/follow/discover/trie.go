package discover

import (
	"os"
	"path/filepath"
	"sort"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
)

// trie returns every real directory whose encoded name is name, sorted.
//
// Encoding keeps length: one UTF-16 code unit in, one byte out, and
// alphanumeric units pass through unchanged. So a directory name can be
// matched against a slice of the encoded name without decoding
// anything: each alphanumeric byte pins the unit, each "-" accepts any
// other unit. The search starts at "/" and descends only into
// directories whose names fit, listing names and nothing else.
// Symbolic links are not followed: Claude Code records the physical
// working directory, so a path through a link never names a project.
func trie(name string) []string {
	type step struct {
		dir string
		i   int
	}
	frontier := []step{{dir: "/", i: 1}}
	var results []string
	for len(frontier) > 0 {
		s := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		if s.i == len(name) {
			results = append(results, s.dir)
			continue
		}
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			seg := utf16.Encode([]rune(norm.NFC.String(e.Name())))
			end := s.i + len(seg)
			if end > len(name) || !classMatch(seg, name[s.i:end]) {
				continue
			}
			p := filepath.Join(s.dir, e.Name())
			switch {
			case end == len(name):
				results = append(results, p)
			case name[end] == '-':
				frontier = append(frontier, step{dir: p, i: end + 1})
			}
		}
	}
	sort.Strings(results)
	return results
}

func classMatch(seg []uint16, chunk string) bool {
	for i, u := range seg {
		if isAlnum(u) {
			if byte(u) != chunk[i] {
				return false
			}
		} else if chunk[i] != '-' {
			return false
		}
	}
	return true
}
