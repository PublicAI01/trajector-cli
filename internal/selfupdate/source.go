package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/semver"
)

// checksumsAsset is the release asset listing every archive's SHA-256.
// The release pipeline names it; both sides must keep saying the same
// thing.
const checksumsAsset = "trajector_checksums.txt"

// binaryName is the entry inside a release archive that becomes the
// installed binary.
const binaryName = "trajector"

// Transfer bounds. A release archive is tens of megabytes, so the
// budget is generous, but no read is unbounded: a source that streams
// forever must fail rather than fill the disk.
const (
	maxArchiveBytes   = 256 << 20
	maxChecksumBytes  = 1 << 20
	maxIndexBytes     = 8 << 20
	indexTimeout      = 30 * time.Second
	downloadTimeout   = 10 * time.Minute
	responseHeaderTTL = 30 * time.Second
)

// errNoRelease reports a release index that names no version this build
// can compare itself against.
var errNoRelease = errors.New("selfupdate: the release index lists no usable version")

// source reads published releases from one release index.
type source struct {
	url    string
	client *http.Client
	agent  string
}

// newSource builds a source reading releases from indexURL, identifying
// itself as this trajector build. The index and every asset it names
// must be a destination that can receive a request safely — https, or
// plain http only when the host can name nothing but this machine —
// because what comes back becomes the binary the user runs next.
func newSource(indexURL, version string) *source {
	s := &source{url: indexURL, agent: platform.UserAgent(version)}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = responseHeaderTTL
	s.client = &http.Client{
		Transport: transport,
		// A release download redirects to wherever the assets are
		// stored, so every hop is vetted, not only the first.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("selfupdate: too many redirects")
			}
			return checkURL(req.URL.String())
		},
	}
	return s
}

// release is one published release, with the two assets an upgrade to
// it needs.
type release struct {
	// version is the release's version without the tag's leading "v",
	// the spelling `trajector version` prints.
	version string
	// archive is the asset for the platform newest was asked about.
	archive asset
	// checksums is the asset holding every archive's SHA-256.
	checksums asset
}

// asset is one downloadable file of a release.
type asset struct {
	name string
	url  string
}

// hostPlatform is the platform this build runs on, the one an upgrade
// must fetch an archive for.
func hostPlatform() (goos, goarch string) { return runtime.GOOS, runtime.GOARCH }

// newest is the highest published release carrying an archive for the
// given platform. Highest is by semantic-version precedence rather
// than by publication order, and pre-releases count: during 0.x every
// release is one, so skipping them would leave nothing to upgrade to.
func (s *source) newest(goos, goarch string) (release, error) {
	if err := checkURL(s.url); err != nil {
		return release{}, err
	}
	body, err := s.get(s.url, indexTimeout, maxIndexBytes)
	if err != nil {
		return release{}, err
	}
	var index []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return release{}, fmt.Errorf("selfupdate: reading the release index: %w", err)
	}

	var best release
	for _, entry := range index {
		if entry.Draft {
			continue
		}
		version := strings.TrimPrefix(entry.TagName, "v")
		if !semver.Comparable(version) {
			continue
		}
		if best.version != "" {
			if order, ok := semver.Compare(version, best.version); !ok || order <= 0 {
				continue
			}
		}
		candidate := release{version: version}
		suffix := "_" + goos + "_" + goarch + archiveExt(goos)
		for _, a := range entry.Assets {
			switch {
			case strings.HasSuffix(a.Name, suffix):
				candidate.archive = asset{name: a.Name, url: a.URL}
			case a.Name == checksumsAsset:
				candidate.checksums = asset{name: a.Name, url: a.URL}
			}
		}
		if candidate.archive.url == "" || candidate.checksums.url == "" {
			continue
		}
		best = candidate
	}
	if best.version == "" {
		return release{}, errNoRelease
	}
	return best, nil
}

// download fetches rel's archive, checks it against rel's checksum
// file, and returns the binary from inside it. Every failure leaves
// the caller with nothing to install rather than something unverified.
func (s *source) download(rel release, goos string) ([]byte, error) {
	sums, err := s.get(rel.checksums.url, indexTimeout, maxChecksumBytes)
	if err != nil {
		return nil, err
	}
	want, err := expectedSum(sums, rel.archive.name)
	if err != nil {
		return nil, err
	}
	archive, err := s.get(rel.archive.url, downloadTimeout, maxArchiveBytes)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("selfupdate: %s does not match its published checksum; nothing was installed", rel.archive.name)
	}
	return extractBinary(archive, goos)
}

// get reads a URL's body with a deadline and a size bound.
func (s *source) get(url string, timeout time.Duration, limit int64) ([]byte, error) {
	if err := checkURL(url); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: %w", err)
	}
	req.Header.Set("User-Agent", s.agent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(url, resp.StatusCode, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: reading %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("selfupdate: %s is larger than the %d-byte limit", url, limit)
	}
	return body, nil
}

// statusError names the two refusals worth telling apart from a plain
// failure: an unpublished release, and a release source that is
// answering but rationing.
func statusError(url string, code int, status string) error {
	switch code {
	case http.StatusNotFound:
		return fmt.Errorf("selfupdate: %s is not published", url)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return fmt.Errorf("selfupdate: the release source is rate limiting this machine (%s); try again later", status)
	}
	return fmt.Errorf("selfupdate: fetching %s: %s", url, status)
}

// checkURL refuses a destination that could hand this machine a binary
// over a connection nobody authenticated. It is the same rule that
// decides where captured data and credentials may go.
func checkURL(url string) error {
	if !platform.CredentialSafeURL(url) {
		return fmt.Errorf("selfupdate: refusing %q: a non-loopback release source must use https", url)
	}
	return nil
}

// expectedSum finds one archive's hash in a checksum file. Only the
// named archive's line is read: the file covers every platform, and
// the absent ones are not failures.
func expectedSum(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		sum, file, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		// The separator is two spaces, and a leading "*" marks a binary
		// read; neither belongs to the name.
		if strings.TrimPrefix(strings.TrimSpace(file), "*") == name {
			return strings.ToLower(sum), nil
		}
	}
	return "", fmt.Errorf("selfupdate: %s has no entry for %s; refusing to install unverified", checksumsAsset, name)
}

// archiveExt is the archive format the release pipeline publishes for
// a platform.
func archiveExt(goos string) string {
	if goos == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

// executableName is what the binary is called inside a release archive
// and on disk.
func executableName(goos string) string {
	if goos == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}

// extractBinary pulls the trajector executable out of a release
// archive. An archive without one is a broken release, not something
// to install part of.
func extractBinary(archive []byte, goos string) ([]byte, error) {
	want := executableName(goos)
	if goos == "windows" {
		return extractZip(archive, want)
	}
	return extractTarGz(archive, want)
}

func extractTarGz(archive []byte, want string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: reading the release archive: %w", err)
	}
	defer gz.Close()
	r := tar.NewReader(gz)
	for {
		header, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("selfupdate: reading the release archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || path.Base(header.Name) != want {
			continue
		}
		return readBounded(r, header.Name)
	}
	return nil, fmt.Errorf("selfupdate: the release archive holds no %s", want)
}

func extractZip(archive []byte, want string) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: reading the release archive: %w", err)
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("selfupdate: reading %s: %w", f.Name, err)
		}
		defer rc.Close()
		return readBounded(rc, f.Name)
	}
	return nil, fmt.Errorf("selfupdate: the release archive holds no %s", want)
}

// readBounded reads one archive entry under the same size bound the
// whole archive gets, so a compressed stream that expands without end
// cannot fill memory.
func readBounded(r io.Reader, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: reading %s: %w", name, err)
	}
	if int64(len(data)) > maxArchiveBytes {
		return nil, fmt.Errorf("selfupdate: %s is larger than the %d-byte limit", name, int64(maxArchiveBytes))
	}
	return data, nil
}
