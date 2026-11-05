//go:build windows

package follow

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/PublicAI01/trajector-cli/internal/winpath"
)

func statFile(path string) (Stat, error) {
	p, err := winpath.Pointer(path)
	if err != nil {
		return Stat{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return Stat{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return Stat{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	var identity struct {
		Volume uint64
		ID     [16]byte
	}
	err = windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity)))
	if err != nil {
		if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) && !errors.Is(err, windows.ERROR_NOT_SUPPORTED) && !errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
			return Stat{}, &os.PathError{Op: "stat", Path: path, Err: err}
		}
		identity.Volume = uint64(info.VolumeSerialNumber)
		binary.LittleEndian.PutUint64(identity.ID[:8], uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow))
	}
	var id [24]byte
	binary.LittleEndian.PutUint64(id[:8], identity.Volume)
	copy(id[8:], identity.ID[:])
	return Stat{
		Exists: true,
		FileID: hex.EncodeToString(id[:]),
		Size:   int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow),
	}, nil
}
