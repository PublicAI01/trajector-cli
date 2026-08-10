// Package fakereleases is a stand-in for the release source the
// upgrade command reads. It publishes real archives — actual gzipped
// tar and zip streams carrying a real binary, with a real checksum
// file — so a test exercises the same download, verify, and unpack
// path a user does, and a change that breaks unpacking cannot pass by
// agreeing with a hand-written fixture.
package fakereleases

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
)

// checksumsAsset is what the release pipeline calls the file listing
// every archive's hash.
const checksumsAsset = "trajector_checksums.txt"

// platforms is every platform the release pipeline builds for. A fake
// release carries all of them, so asset selection is exercised against
// a list where five of six entries are the wrong answer.
var platforms = []struct{ OS, Arch string }{
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"windows", "amd64"},
	{"windows", "arm64"},
}

// Server is the fake release source, shut down with the test.
type Server struct {
	HTTP *httptest.Server

	mu        sync.Mutex
	releases  []*release
	downloads []string
	rationing bool
}

// release is one published release and its assets by name.
type release struct {
	tag    string
	draft  bool
	assets map[string][]byte
}

// New starts the fake release source.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	s.HTTP = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.HTTP.Close)
	return s
}

// IndexURL is the release index, the address a client is pointed at.
func (s *Server) IndexURL() string { return s.HTTP.URL + "/releases" }

// APIBase is the origin the install script is pointed at. That script
// is given an origin rather than a URL because it builds the index path
// itself, out of the repository slug compiled into it — so the fake
// answers the index under a repository path as well as at IndexURL.
func (s *Server) APIBase() string { return s.HTTP.URL }

// AssetBase is the origin release assets hang under, the shape the
// install script joins a tag and an asset name onto.
func (s *Server) AssetBase() string { return s.HTTP.URL + "/download" }

// Publish adds a release of the given version — tagged "v" plus the
// version, the way the pipeline tags — carrying body as the binary
// inside every platform's archive, and a checksum file that matches.
func (s *Server) Publish(t *testing.T, version string, body []byte) {
	t.Helper()
	s.PublishTag(t, "v"+version, version, body)
}

// PublishTag adds a release under an arbitrary tag, for the cases the
// tag and the version are worth separating: a tag no version can be
// read out of, or one that does not begin with "v".
func (s *Server) PublishTag(t *testing.T, tag, version string, body []byte) {
	t.Helper()
	s.publish(t, tag, version, func(goos string) []byte { return pack(t, goos, body) })
}

// PublishBroken adds a release whose archives are intact as far as
// their checksums go but are not archives at all — a pipeline that
// uploaded the wrong bytes. Verification passes and unpacking is what
// has to catch it.
func (s *Server) PublishBroken(t *testing.T, version string) {
	t.Helper()
	s.publish(t, "v"+version, version, func(string) []byte {
		return []byte("this release's archives were never archives")
	})
}

func (s *Server) publish(t *testing.T, tag, version string, build func(goos string) []byte) {
	t.Helper()
	rel := &release{tag: tag, assets: map[string][]byte{}}
	var sums strings.Builder
	for _, p := range platforms {
		name := archiveName(version, p.OS, p.Arch)
		archive := build(p.OS)
		rel.assets[name] = archive
		sum := sha256.Sum256(archive)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	rel.assets[checksumsAsset] = []byte(sums.String())

	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = append(s.releases, rel)
}

// PublishWithout adds a release missing one asset, for a release the
// pipeline only half finished — no checksum file, or no archive for
// some platform.
func (s *Server) PublishWithout(t *testing.T, version string, body []byte, asset string) {
	t.Helper()
	s.Publish(t, version, body)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.releases[len(s.releases)-1].assets, asset)
}

// PublishDraft adds a release that has been prepared but not
// published. A draft's assets are not downloadable and no client may
// upgrade to it.
func (s *Server) PublishDraft(t *testing.T, version string, body []byte) {
	t.Helper()
	s.Publish(t, version, body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases[len(s.releases)-1].draft = true
}

// Corrupt replaces the content served for one platform's archive of a
// version, leaving the published checksum alone: what arrives no
// longer hashes to what the release says it should, exactly as a
// tampered or truncated download would.
func (s *Server) Corrupt(t *testing.T, version, goos, goarch string) {
	t.Helper()
	name := archiveName(version, goos, goarch)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rel := range s.releases {
		if _, ok := rel.assets[name]; !ok {
			continue
		}
		rel.assets[name] = []byte("this is not the archive that was published")
		return
	}
	t.Fatalf("fakereleases: no published release carries %s", name)
}

// Ration makes the source answer every further request the way a
// release host answers a machine that has asked too often.
func (s *Server) Ration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rationing = true
}

// Downloads is the assets fetched so far, in order. A test asserting
// that some path installs nothing asserts on this being empty.
func (s *Server) Downloads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.downloads...)
}

// ArchiveName is the asset name the pipeline gives one platform's
// archive of a version.
func ArchiveName(version, goos, goarch string) string {
	return archiveName(version, goos, goarch)
}

func archiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("trajector_%s_%s_%s%s", version, goos, goarch, ext)
}

// pack builds the archive format the pipeline publishes for a platform,
// holding body as the trajector binary. A companion LICENSE entry rides
// along the way the real archives carry one, so unpacking has to pick
// the binary out rather than take whatever it finds first.
func pack(t *testing.T, goos string, body []byte) []byte {
	t.Helper()
	name := "trajector"
	if goos == "windows" {
		name += ".exe"
	}
	if goos == "windows" {
		return packZip(t, name, body)
	}
	return packTarGz(t, name, body)
}

func packTarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(entry string, content []byte, mode int64) {
		header := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     entry,
			Mode:     mode,
			Size:     int64(len(content)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("fakereleases: writing %s: %v", entry, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("fakereleases: writing %s: %v", entry, err)
		}
	}
	write("LICENSE", []byte("license text\n"), 0o644)
	write(name, body, 0o755)
	if err := tw.Close(); err != nil {
		t.Fatalf("fakereleases: closing the archive: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("fakereleases: closing the archive: %v", err)
	}
	return buf.Bytes()
}

func packZip(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(entry string, content []byte) {
		w, err := zw.Create(entry)
		if err != nil {
			t.Fatalf("fakereleases: writing %s: %v", entry, err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("fakereleases: writing %s: %v", entry, err)
		}
	}
	write("LICENSE", []byte("license text\n"))
	write(name, body)
	if err := zw.Close(); err != nil {
		t.Fatalf("fakereleases: closing the archive: %v", err)
	}
	return buf.Bytes()
}

// serve answers the two shapes a release source has: the index of
// published releases, and one asset of one release.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rationing {
		http.Error(w, "rate limit exceeded", http.StatusForbidden)
		return
	}
	// Any path ending in /releases is the index: clients handed a full
	// URL ask for IndexURL, while a client given only an origin builds
	// the real source's /repos/<owner>/<repo>/releases path itself.
	if strings.HasSuffix(r.URL.Path, "/releases") {
		s.serveIndex(w)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/download/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	tag, name := path.Split(rest)
	tag = strings.TrimSuffix(tag, "/")
	for _, rel := range s.releases {
		if rel.tag != tag || rel.draft {
			continue
		}
		asset, ok := rel.assets[name]
		if !ok {
			break
		}
		s.downloads = append(s.downloads, name)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(asset)
		return
	}
	http.NotFound(w, r)
}

// serveIndex writes the release list newest-published first, the order
// the real source uses — which is not the order of version precedence,
// so a client that takes the first entry rather than the highest
// version is caught by any test that publishes out of order.
func (s *Server) serveIndex(w http.ResponseWriter) {
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}
	type entry struct {
		TagName    string  `json:"tag_name"`
		Draft      bool    `json:"draft"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}
	index := []entry{}
	for i := len(s.releases) - 1; i >= 0; i-- {
		rel := s.releases[i]
		e := entry{
			TagName: rel.tag,
			Draft:   rel.draft,
			// Every 0.x release is published as a pre-release, and the
			// upgrade path must still find them.
			Prerelease: strings.HasPrefix(strings.TrimPrefix(rel.tag, "v"), "0."),
		}
		for name := range rel.assets {
			e.Assets = append(e.Assets, asset{
				Name: name,
				URL:  s.HTTP.URL + "/download/" + rel.tag + "/" + name,
			})
		}
		index = append(index, e)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(index); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
