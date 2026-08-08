package tokenstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// fileStore keeps each secret in its own owner-only file so that a
// partially failed write can never corrupt other secrets.
type fileStore struct {
	dir string
}

func (s fileStore) path(name string) string {
	return filepath.Join(s.dir, name+".secret")
}

func (s fileStore) Save(name string, secret []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := userdirs.EnsureOwnerDir(s.dir); err != nil {
		return err
	}
	return fsatomic.WriteFile(s.path(name), secret, 0o600)
}

func (s fileStore) Load(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	secret, err := fsatomic.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNotFound
	}
	return secret, err
}

func (s fileStore) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	err := os.Remove(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return errNotFound
	}
	return err
}
