package tokenstore_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
)

func TestFileStoreRoundTrip(t *testing.T) {
	s := tokenstore.Files(filepath.Join(t.TempDir(), "secrets"))
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
	s := tokenstore.Files(t.TempDir())
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
	s := tokenstore.Files(t.TempDir())
	if _, err := s.Load("absent"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("Load(absent) = %v, want ErrNotFound", err)
	}
}

func TestFileStoreDelete(t *testing.T) {
	s := tokenstore.Files(t.TempDir())
	if err := s.Save("n", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("n"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load("n"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete("n"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestFileStoreOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not enforced on windows")
	}
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := tokenstore.Files(dir).Save("n", []byte("v")); err != nil {
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
	s := tokenstore.Files(t.TempDir())
	for _, name := range []string{"", ".", "..", "../escape", "a/b", `a\b`, ".hidden", "-flag"} {
		t.Run(name, func(t *testing.T) {
			if err := s.Save(name, []byte("v")); err == nil {
				t.Errorf("Save(%q) succeeded, want error", name)
			}
			if _, err := s.Load(name); err == nil || errors.Is(err, tokenstore.ErrNotFound) {
				t.Errorf("Load(%q) = %v, want validation error", name, err)
			}
			if err := s.Delete(name); err == nil || errors.Is(err, tokenstore.ErrNotFound) {
				t.Errorf("Delete(%q) = %v, want validation error", name, err)
			}
		})
	}
}

// unavailable is a real store that cannot work: its root is a plain
// file, so every write fails the way an absent keyring does. The
// fallback is exercised against two genuine adapters, never a stand-in.
func unavailable(t *testing.T) tokenstore.Store {
	t.Helper()
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tokenstore.Files(filepath.Join(blocked, "secrets"))
}

func TestFallsBackWhenThePrimaryIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	s := tokenstore.Open(dir, unavailable(t))

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
	if _, err := s.Load("token"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
}

func TestPrimaryIsPreferredWhenItWorks(t *testing.T) {
	primaryDir, dir := t.TempDir(), t.TempDir()
	s := tokenstore.Open(dir, tokenstore.Files(primaryDir))

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
	if err := tokenstore.Files(dir).Save("token", []byte("stale")); err != nil {
		t.Fatal(err)
	}
	s := tokenstore.Open(dir, tokenstore.Files(primaryDir))
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
	if err := tokenstore.Files(dir).Save("token", []byte("filed")); err != nil {
		t.Fatal(err)
	}
	got, err := tokenstore.Open(dir, tokenstore.Files(primaryDir)).Load("token")
	if err != nil || string(got) != "filed" {
		t.Errorf("Load = %q, %v, want %q, nil", got, err, "filed")
	}
}

func TestDeleteRemovesBothCopies(t *testing.T) {
	primaryDir, dir := t.TempDir(), t.TempDir()
	if err := tokenstore.Files(primaryDir).Save("token", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := tokenstore.Files(dir).Save("token", []byte("b")); err != nil {
		t.Fatal(err)
	}
	s := tokenstore.Open(dir, tokenstore.Files(primaryDir))
	if err := s.Delete("token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load("token"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissingEverywhere(t *testing.T) {
	s := tokenstore.Open(t.TempDir(), tokenstore.Files(t.TempDir()))
	if err := s.Delete("absent"); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("Delete(absent) = %v, want ErrNotFound", err)
	}
}
