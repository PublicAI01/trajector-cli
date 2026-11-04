package fsatomic

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/PublicAI01/trajector-cli/internal/winpath"
)

var fileSecurity = sync.OnceValues(func() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)")
})

func createTemp(dir, prefix string) (*os.File, error) {
	sd, err := fileSecurity()
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for range 100 {
		path := filepath.Join(dir, prefix+rand.Text())
		p, err := winpath.Pointer(path)
		if err != nil {
			return nil, err
		}
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0,
			&sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, &os.PathError{Op: "create", Path: path, Err: err}
		}
		return os.NewFile(uintptr(h), path), nil
	}
	return nil, &os.PathError{Op: "create", Path: dir, Err: fs.ErrExist}
}

func setTempPermissions(tmp, path string, perm fs.FileMode) error {
	var err error
	path, err = winpath.Extended(path)
	if err != nil {
		return err
	}
	tmp, err = winpath.Extended(tmp)
	if err != nil {
		return err
	}
	if perm.Perm()&0o077 != 0 {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			acl, _, err := sd.DACL()
			if err != nil {
				return err
			}
			control, _, err := sd.Control()
			if err != nil {
				return err
			}
			flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
			if control&windows.SE_DACL_PROTECTED != 0 {
				flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
			}
			if err := windows.SetNamedSecurityInfo(tmp, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil); err != nil {
				return err
			}
		}
	}
	return os.Chmod(tmp, perm)
}
