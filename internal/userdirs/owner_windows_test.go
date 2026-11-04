package userdirs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOwnerDirectoryProtectsStateFromInheritedPublicAccess(t *testing.T) {
	parent := t.TempDir()
	public, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := public.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	for _, existing := range []bool{false, true} {
		dir := filepath.Join(parent, "new", "state")
		if existing {
			dir = filepath.Join(parent, "existing")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if err := EnsureOwnerDir(dir); err != nil {
				t.Fatal(err)
			}
		}
		file := filepath.Join(dir, "secret")
		if err := os.WriteFile(file, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{dir, file} {
			sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			sddl := sd.String()
			if strings.Contains(sddl, ";;;WD)") || strings.Contains(sddl, ";;;BU)") || strings.Contains(sddl, ";;;AU)") {
				t.Fatalf("private state inherited public access: %s", sddl)
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sddl, ";;;"+user.User.Sid.String()+")") {
				t.Fatal("private state does not grant its user access")
			}
		}
	}
}
