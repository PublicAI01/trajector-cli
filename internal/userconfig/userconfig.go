// Package userconfig reads the user config file: the options a user
// sets for this device by hand. Every option is read from that file
// alone and never from an environment variable: a repository's
// committed settings reach this process's environment through the
// session hooks, and must not be able to choose where data goes, where
// a replacement binary comes from, or what is uploaded.
//
// One rule covers a file that exists and cannot be read — cut, not
// JSON, not readable by this user: Read returns the Config a failed
// read gives, never a part of the file that did read, and the failure
// beside it as a ReadError. That Config holds every default except
// one: image and document upload is off (see failedRead). What a
// caller does with the failure is the caller's to state, and it is
// never silent: a command refuses to run on it, since the defaults
// could send data or trust a place the user changed away from; status
// shows it; the running proxy logs it. The proxy reads the file again
// for every batch, so while the file cannot be read images and
// documents go up as placeholders, and they go up as the file says
// once it reads again. An option the proxy reads once at start, such
// as platform_url, keeps the value it read: the proxy does not start
// on a file that cannot be read.
package userconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// UploadImagesAndDocumentsKey is the name the file gives the image and
// document switch.
const UploadImagesAndDocumentsKey = "upload_images_and_documents"

// UnreadableConsequence states, after the ReadError that names the
// file, what a file that cannot be read does to a running proxy.
const UnreadableConsequence = "images and documents go up as placeholders until it reads"

// Config is what the user config file may say. The zero Config holds
// every default.
type Config struct {
	// PlatformURL and ReleasesURL each name a place this machine will
	// trust with something: where captured data and the device token go,
	// and where the next binary comes from. Empty means the default.
	PlatformURL string `json:"platform_url"`
	ReleasesURL string `json:"releases_url"`
	// UploadImagesAndDocuments set to false replaces the content of every
	// image and document with a placeholder before upload. Absent means
	// true: it is uploaded.
	UploadImagesAndDocuments *bool `json:"upload_images_and_documents"`
}

// OmitsImagesAndDocuments reports whether the user turned image and
// document upload off.
func (c Config) OmitsImagesAndDocuments() bool {
	return c.UploadImagesAndDocuments != nil && !*c.UploadImagesAndDocuments
}

// ReadError reports a config file that exists and could not be read.
// The Config returned with it is the one failedRead gives.
type ReadError struct {
	Path string
	Err  error
}

func (e *ReadError) Error() string { return fmt.Sprintf("reading %s: %v", e.Path, e.Err) }

func (e *ReadError) Unwrap() error { return e.Err }

// Read reads the user config file at path. An absent file is the
// defaults and no error. A file that cannot be read is failedRead and
// a ReadError — never a part of the file that did read: an option read
// from a file that failed half way is still one the user may have
// meant to change.
func Read(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return failedRead(), &ReadError{Path: path, Err: err}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return failedRead(), &ReadError{Path: path, Err: err}
	}
	return cfg, nil
}

// failedRead is the Config of a file that cannot be read: every option
// at its default, except that image and document upload is off. The
// two ways to guess wrong do not cost the same: an upload sends what a
// user who turned upload off refused to send, and it cannot be taken
// back; a placeholder only withholds content a user who left upload on
// would have sent. An option added later takes its default here unless
// the same holds for it.
func failedRead() Config {
	return Config{UploadImagesAndDocuments: new(false)}
}
