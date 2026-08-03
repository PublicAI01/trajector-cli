package tokenstore

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// keyringStore adapts the OS keyring (Keychain, Secret Service, or
// Windows Credential Manager) to the Store interface.
type keyringStore struct{}

func (keyringStore) Save(name string, secret []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	return keyring.Set(keyringService, name, string(secret))
}

func (keyringStore) Load(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	secret, err := keyring.Get(keyringService, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return []byte(secret), nil
}

func (keyringStore) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	err := keyring.Delete(keyringService, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return errNotFound
	}
	return err
}
