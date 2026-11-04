package fsatomic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

func TestAtomicWriteSupportsLongPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("long-directory/", 24))
	if err := userdirs.EnsureOwnerDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	for _, want := range []string{"first", "replacement"} {
		if err := WriteFile(path, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("ReadFile(long path) = %q, %v", got, err)
		}
	}
}

func TestPrivateWriteIsProtectedBeforeItsFirstFlush(t *testing.T) {
	dir := t.TempDir()
	public, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := public.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	oldSync := syncFile
	t.Cleanup(func() { syncFile = oldSync })
	checked := false
	syncFile = func(f *os.File) error {
		sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		if strings.Contains(sd.String(), ";;;WD)") {
			t.Fatal("the temporary file exposes private contents before rename")
		}
		checked = true
		return f.Sync()
	}
	path := filepath.Join(dir, "settings.local.json")
	if err := WriteFile(path, []byte("synthetic secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("no protected write was observed")
	}
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("edited without widening permissions"), 0o666); err != nil {
		t.Fatal(err)
	}
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || strings.Replace(after.String(), "D:PAI", "D:P", 1) != before.String() {
		t.Fatalf("an ordinary edit did not retain the file ACL: %v; before %s, after %s", err, before.String(), after.String())
	}
}
