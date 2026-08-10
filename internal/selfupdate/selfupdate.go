// Package selfupdate moves this installation to a newer published
// release: which release that is, fetching and verifying its archive,
// and swapping the running binary for the one inside. It decides
// nothing the user reads — the caller owns every sentence — and it
// never replaces a binary it has not verified.
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
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

// DefaultReleasesURL is where published releases are listed. The
// /releases/latest endpoint is deliberately not used: it omits
// pre-releases, and every 0.x release is published as one.
const DefaultReleasesURL = "https://api.github.com/repos/PublicAI01/trajector-cli/releases"

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

// ErrNoRelease reports a release index that names no version this
// build can compare itself against.
var ErrNoRelease = errors.New("selfupdate: the release index lists no usable version")

// Source reads published releases from one release index.
type Source struct {
	url    string
	client *http.Client
	agent  string
}

// New builds a source reading releases from indexURL, identifying
// itself as this trajector build. The index and every asset it names
// must be a destination that can receive a request safely — https, or
// plain http only when the host can name nothing but this machine —
// because what comes back becomes the binary the user runs next.
func New(indexURL, version string) *Source {
	s := &Source{url: indexURL, agent: platform.UserAgent(version)}
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

// Release is one published release, with the two assets an upgrade to
// it needs.
type Release struct {
	// Version is the release's version without the tag's leading "v",
	// the spelling `trajector version` prints.
	Version string
	// Archive is the asset for the platform Newest was asked about.
	Archive Asset
	// Checksums is the asset holding every archive's SHA-256.
	Checksums Asset
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string
	URL  string
}

// Newest is the highest published release carrying an archive for the
// given platform. Highest is by semantic-version precedence rather
// than by publication order, and pre-releases count: during 0.x every
// release is one, so skipping them would leave nothing to upgrade to.
func (s *Source) Newest(goos, goarch string) (Release, error) {
	if err := checkURL(s.url); err != nil {
		return Release{}, err
	}
	body, err := s.get(s.url, indexTimeout, maxIndexBytes)
	if err != nil {
		return Release{}, err
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
		return Release{}, fmt.Errorf("selfupdate: reading the release index: %w", err)
	}

	var best Release
	for _, entry := range index {
		if entry.Draft {
			continue
		}
		version := strings.TrimPrefix(entry.TagName, "v")
		if _, ok := proxylife.Compare(version, version); !ok {
			continue
		}
		if best.Version != "" {
			if order, ok := proxylife.Compare(version, best.Version); !ok || order <= 0 {
				continue
			}
		}
		candidate := Release{Version: version}
		suffix := "_" + goos + "_" + goarch + archiveExt(goos)
		for _, a := range entry.Assets {
			switch {
			case strings.HasSuffix(a.Name, suffix):
				candidate.Archive = Asset{Name: a.Name, URL: a.URL}
			case a.Name == checksumsAsset:
				candidate.Checksums = Asset{Name: a.Name, URL: a.URL}
			}
		}
		if candidate.Archive.URL == "" || candidate.Checksums.URL == "" {
			continue
		}
		best = candidate
	}
	if best.Version == "" {
		return Release{}, ErrNoRelease
	}
	return best, nil
}

// Download fetches rel's archive, checks it against rel's checksum
// file, and returns the binary from inside it. Every failure leaves
// the caller with nothing to install rather than something unverified.
func (s *Source) Download(rel Release, goos string) ([]byte, error) {
	sums, err := s.get(rel.Checksums.URL, indexTimeout, maxChecksumBytes)
	if err != nil {
		return nil, err
	}
	want, err := expectedSum(sums, rel.Archive.Name)
	if err != nil {
		return nil, err
	}
	archive, err := s.get(rel.Archive.URL, downloadTimeout, maxArchiveBytes)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("selfupdate: %s does not match its published checksum; nothing was installed", rel.Archive.Name)
	}
	return extractBinary(archive, goos)
}

// get reads a URL's body with a deadline and a size bound.
func (s *Source) get(url string, timeout time.Duration, limit int64) ([]byte, error) {
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

// HostPlatform is the platform this build runs on, the one an upgrade
// must fetch an archive for.
func HostPlatform() (goos, goarch string) { return runtime.GOOS, runtime.GOARCH }

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
