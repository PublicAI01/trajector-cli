package tokenstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	s := fileStore{dir: filepath.Join(t.TempDir(), "secrets")}
	if err := s.Save("device-token", []byte("sk-test-fake")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("device-token")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got, []byte("sk-test-fake")) {
		t.Errorf("Load = %q, want %q", got, "sk-test-fake")
	}
}

func TestFileStoreOverwrite(t *testing.T) {
	s := fileStore{dir: t.TempDir()}
	if err := s.Save("n", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("n", []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("n")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("Load = %q, want %q", got, "new")
	}
}

func TestFileStoreLoadMissing(t *testing.T) {
	s := fileStore{dir: t.TempDir()}
	if _, err := s.Load("absent"); !errors.Is(err, errNotFound) {
		t.Errorf("Load(absent) = %v, want ErrNotFound", err)
	}
}

func TestFileStoreDelete(t *testing.T) {
	s := fileStore{dir: t.TempDir()}
	if err := s.Save("n", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("n"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load("n"); !errors.Is(err, errNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete("n"); !errors.Is(err, errNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestFileStoreOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not enforced on windows")
	}
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := (fileStore{dir: dir}).Save("n", []byte("v")); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "n.secret"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
}

func TestSecretNameValidation(t *testing.T) {
	s := fileStore{dir: t.TempDir()}
	for _, name := range []string{"", ".", "..", "../escape", "a/b", `a\b`, ".hidden", "-flag"} {
		t.Run(name, func(t *testing.T) {
			if err := s.Save(name, []byte("v")); err == nil {
				t.Errorf("Save(%q) succeeded, want error", name)
			}
			if _, err := s.Load(name); err == nil || errors.Is(err, errNotFound) {
				t.Errorf("Load(%q) = %v, want validation error", name, err)
			}
			if err := s.Delete(name); err == nil || errors.Is(err, errNotFound) {
				t.Errorf("Delete(%q) = %v, want validation error", name, err)
			}
		})
	}
}

// unavailable is a real store that cannot work: its root is a plain
// file, so every write fails the way an absent keyring does. The
// fallback is exercised against two genuine adapters, never a stand-in.
func unavailable(t *testing.T) backend {
	t.Helper()
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fileStore{dir: filepath.Join(blocked, "secrets")}
}

func TestFallsBackWhenThePrimaryIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	s := &fallbackStore{primary: unavailable(t), fallback: fileStore{dir: dir}}

	if err := s.Save("token", []byte("v")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.secret")); err != nil {
		t.Errorf("secret not written to the fallback: %v", err)
	}
	got, err := s.Load("token")
	if err != nil || string(got) != "v" {
		t.Errorf("Load = %q, %v, want %q, nil", got, err, "v")
	}
	if err := s.Delete("token"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if _, err := s.Load("token"); !errors.Is(err, errNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
}

func TestPrimaryIsPreferredWhenItWorks(t *testing.T) {
	primaryDir, dir := t.TempDir(), t.TempDir()
	s := &fallbackStore{primary: fileStore{dir: primaryDir}, fallback: fileStore{dir: dir}}

	if err := s.Save("token", []byte("v")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(primaryDir, "token.secret")); err != nil {
		t.Errorf("secret not stored in the primary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.secret")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("secret unexpectedly in the fallback: %v", err)
	}
}

func TestSavingToThePrimaryRemovesAStaleFallbackCopy(t *testing.T) {
	primaryDir, dir := t.TempDir(), t.TempDir()
	if err := (fileStore{dir: dir}).Save("token", []byte("stale")); err != nil {
		t.Fatal(err)
	}
	s := &fallbackStore{primary: fileStore{dir: primaryDir}, fallback: fileStore{dir: dir}}
	if err := s.Save("token", []byte("fresh")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.secret")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale fallback copy still present: %v", err)
	}
	got, err := s.Load("token")
	if err != nil || string(got) != "fresh" {
		t.Errorf("Load = %q, %v, want %q, nil", got, err, "fresh")
	}
}

func TestLoadFallsThroughToTheFallbackCopy(t *testing.T) {
	primaryDir, dir := t.TempDir(), t.TempDir()
	if err := (fileStore{dir: dir}).Save("token", []byte("filed")); err != nil {
		t.Fatal(err)
	}
	got, err := (&fallbackStore{primary: fileStore{dir: primaryDir}, fallback: fileStore{dir: dir}}).Load("token")
	if err != nil || string(got) != "filed" {
		t.Errorf("Load = %q, %v, want %q, nil", got, err, "filed")
	}
}

func TestDeleteRemovesBothCopies(t *testing.T) {
	primaryDir, dir := t.TempDir(), t.TempDir()
	if err := (fileStore{dir: primaryDir}).Save("token", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := (fileStore{dir: dir}).Save("token", []byte("b")); err != nil {
		t.Fatal(err)
	}
	s := &fallbackStore{primary: fileStore{dir: primaryDir}, fallback: fileStore{dir: dir}}
	if err := s.Delete("token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load("token"); !errors.Is(err, errNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissingEverywhere(t *testing.T) {
	s := &fallbackStore{primary: fileStore{dir: t.TempDir()}, fallback: fileStore{dir: t.TempDir()}}
	if err := s.Delete("absent"); !errors.Is(err, errNotFound) {
		t.Errorf("Delete(absent) = %v, want ErrNotFound", err)
	}
}

// stubBackend stands in for an OS keyring that is present and readable
// but will not take a write or a delete — the state a desktop keyring
// is in for a headless session on the same machine.
type stubBackend struct {
	secrets   map[string][]byte
	saveErr   error
	deleteErr error
}

func newStub(name, secret string) *stubBackend {
	return &stubBackend{secrets: map[string][]byte{name: []byte(secret)}}
}

func (b *stubBackend) Save(name string, secret []byte) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	b.secrets[name] = append([]byte(nil), secret...)
	return nil
}

func (b *stubBackend) Load(name string) ([]byte, error) {
	secret, ok := b.secrets[name]
	if !ok {
		return nil, errNotFound
	}
	return secret, nil
}

func (b *stubBackend) Delete(name string) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	if _, ok := b.secrets[name]; !ok {
		return errNotFound
	}
	delete(b.secrets, name)
	return nil
}

// TestSaveToTheFallbackClearsAShadowingPrimaryCopy pins the cleanup that
// was only ever done in one direction. Load prefers the primary, so a
// copy left in a keyring that has since become unwritable hides every
// later save.
func TestSaveToTheFallbackClearsAShadowingPrimaryCopy(t *testing.T) {
	primary := newStub("token", "old")
	primary.saveErr = errors.New("keyring is unavailable")
	s := &fallbackStore{primary: primary, fallback: fileStore{dir: t.TempDir()}}

	if err := s.Save("token", []byte("new")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("token")
	if err != nil || string(got) != "new" {
		t.Errorf("Load = %q, %v, want %q, nil: the primary's stale copy shadows the saved secret", got, err, "new")
	}
}

// TestSaveFailsWhileAnUnwritablePrimaryStillShadowsTheSecret pins the
// case the shadow-clearing above cannot cover on its own: a keyring that
// refuses a write refuses the delete too, so the cleanup runs and
// achieves nothing. Answering success there let `trajector login` report
// a fresh device token while every later read handed back the old one.
func TestSaveFailsWhileAnUnwritablePrimaryStillShadowsTheSecret(t *testing.T) {
	primary := newStub("token", "old")
	primary.saveErr = errors.New("keyring is unavailable")
	primary.deleteErr = primary.saveErr
	dir := t.TempDir()
	s := &fallbackStore{primary: primary, fallback: fileStore{dir: dir}}

	if err := s.Save("token", []byte("new")); err == nil {
		t.Fatal("Save reported success while the primary still hands an older secret back")
	}
	if got, err := s.Load("token"); err != nil || string(got) != "old" {
		t.Errorf("Load = %q, %v; the shadowing copy is still what a reader gets", got, err)
	}
	// The secret did reach the fallback: refusing to claim success must
	// not also throw away the write, or retrying after unlocking the
	// keyring would have nothing to fall back on.
	if got, err := (fileStore{dir: dir}).Load("token"); err != nil || string(got) != "new" {
		t.Errorf("fallback copy = %q, %v, want %q, nil", got, err, "new")
	}
}

// TestDeleteFailsWhileThePrimaryStillHoldsTheSecret pins that a
// half-completed delete is reported as one. Answering success because
// the fallback copy went let `trajector logout` say the device was
// signed out while a still-valid, never-revoked token sat in the keyring.
func TestDeleteFailsWhileThePrimaryStillHoldsTheSecret(t *testing.T) {
	primary := newStub("token", "old")
	primary.deleteErr = errors.New("keyring entry is locked")
	dir := t.TempDir()
	if err := (fileStore{dir: dir}).Save("token", []byte("filed")); err != nil {
		t.Fatal(err)
	}
	s := &fallbackStore{primary: primary, fallback: fileStore{dir: dir}}

	if err := s.Delete("token"); err == nil {
		t.Fatal("Delete reported success while the primary still hands the secret back")
	}
	if got, err := s.Load("token"); err != nil || string(got) != "old" {
		t.Errorf("Load = %q, %v; the secret Delete could not remove is still there", got, err)
	}
}
