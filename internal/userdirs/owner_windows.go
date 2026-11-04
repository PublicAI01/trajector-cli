package userdirs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/PublicAI01/trajector-cli/internal/winpath"
)

var ownerSecurity = sync.OnceValues(func() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
})

func ensureOwnerDir(dir string) error {
	dir, err := winpath.Extended(dir)
	if err != nil {
		return err
	}
	sd, err := ownerSecurity()
	if err != nil {
		return err
	}
	if err := mkdirOwnerAll(filepath.Clean(dir), sd); err != nil {
		return err
	}
	current, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return &os.PathError{Op: "permissions", Path: dir, Err: err}
	}
	if strings.Replace(current.String(), "D:PAI", "D:P", 1) == sd.String() {
		return nil
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return &os.PathError{Op: "permissions", Path: dir, Err: err}
	}
	return nil
}

func mkdirOwnerAll(dir string, sd *windows.SECURITY_DESCRIPTOR) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return &os.PathError{Op: "mkdir", Path: dir, Err: windows.ERROR_DIRECTORY}
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return err
	}
	if err := mkdirOwnerAll(parent, sd); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(p, &sa); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
				return nil
			}
		}
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}
