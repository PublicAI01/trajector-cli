//go:build windows

package fsatomic

import (
	"encoding/binary"
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// Constants the syscall package does not name.
const (
	errSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
	accessDelete        = 0x00010000        // DELETE

	// SetFileInformationByHandle rename with POSIX semantics: the
	// destination is superseded even while shared-delete handles hold
	// it open, which MoveFileEx refuses.
	fileRenameInfoEx              = 22
	fileRenameFlagReplaceIfExists = 0x1
	fileRenameFlagPosixSemantics  = 0x2
)

// kernel32 is on the KnownDLLs list, so this lazy load resolves from
// the already-mapped system copy, never a search path.
var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetFileInformationByHandle = kernel32.NewProc("SetFileInformationByHandle")
)

// replaceCollision reports the two transient failures Windows produces
// when an open and a rename-replace of one path cross: the opener is
// refused while the rename holds the name, and the renamer is denied
// while a handle without delete sharing holds the destination.
func replaceCollision(err error) bool {
	return errors.Is(err, errSharingViolation) || errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}

// openShared opens path for reading with delete sharing granted, which
// os.Open withholds, so a concurrent WriteFile can replace the file
// while this handle is still reading the previous content.
func openShared(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// renameReplace replaces newpath with oldpath, preferring POSIX
// semantics so the replace succeeds while openShared readers hold the
// destination. Windows versions and filesystems without the support
// fall through to os.Rename, whose collisions the caller retries.
func renameReplace(oldpath, newpath string) error {
	err := renamePosix(oldpath, newpath)
	if err == nil || replaceCollision(err) {
		return err
	}
	return os.Rename(oldpath, newpath)
}

func renamePosix(oldpath, newpath string) error {
	wrap := func(err error) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	src, err := syscall.UTF16PtrFromString(oldpath)
	if err != nil {
		return wrap(err)
	}
	h, err := syscall.CreateFile(src,
		accessDelete,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return wrap(err)
	}
	defer syscall.CloseHandle(h)

	dst, err := syscall.UTF16FromString(newpath)
	if err != nil {
		return wrap(err)
	}
	// FILE_RENAME_INFO, packed by hand: DWORD Flags, then a
	// pointer-aligned HANDLE RootDirectory, DWORD FileNameLength in
	// bytes without the terminator, and the WCHAR name.
	const ptrSize = int(unsafe.Sizeof(uintptr(0)))
	lenOff := 2 * ptrSize
	nameOff := lenOff + 4
	buf := make([]byte, nameOff+2*len(dst))
	binary.LittleEndian.PutUint32(buf[0:], fileRenameFlagReplaceIfExists|fileRenameFlagPosixSemantics)
	binary.LittleEndian.PutUint32(buf[lenOff:], uint32(2*(len(dst)-1)))
	for i, c := range dst {
		binary.LittleEndian.PutUint16(buf[nameOff+2*i:], c)
	}
	r1, _, callErr := procSetFileInformationByHandle.Call(
		uintptr(h), fileRenameInfoEx, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r1 == 0 {
		return wrap(callErr)
	}
	return nil
}
