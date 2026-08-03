package tokenstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

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
	// Write-then-rename keeps the stored secret intact if the process
	// dies mid-write.
	tmp, err := os.CreateTemp(s.dir, name+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(secret); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(name))
}

func (s fileStore) Load(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	secret, err := os.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return secret, err
}

func (s fileStore) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	err := os.Remove(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}
