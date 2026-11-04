package lifecycle_test

import (
	"fmt"
	"io/fs"
	"testing"

	"golang.org/x/sys/windows"
)

func restrictDirectory(t *testing.T, dir string, mode fs.FileMode) {
	t.Helper()
	previous, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var denied uint32
	if mode&0o400 == 0 {
		denied |= windows.FILE_LIST_DIRECTORY
	}
	if mode&0o200 == 0 {
		const deleteChild = 0x40
		denied |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | deleteChild
	}
	sid := user.User.Sid.String()
	restricted, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(D;;0x%x;;;%s)(A;OICI;FA;;;%s)", denied, sid, sid))
	if err != nil {
		t.Fatal(err)
	}
	set := func(sd *windows.SECURITY_DESCRIPTOR) {
		t.Helper()
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := sd.Control()
		if err != nil {
			t.Fatal(err)
		}
		flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
		if control&windows.SE_DACL_PROTECTED != 0 {
			flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
		}
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { set(previous) })
	set(restricted)
}
