//go:build windows

package fileholders

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Constants the syscall package does not name.
const (
	cchRmSessionKey                = 32 // CCH_RM_SESSION_KEY
	cchRmMaxAppName                = 255
	cchRmMaxSvcName                = 63
	errMoreData                    = syscall.Errno(234) // ERROR_MORE_DATA
	processQueryLimitedInformation = 0x1000
)

// rmProcessInfo is RM_PROCESS_INFO: an RM_UNIQUE_PROCESS (a DWORD and a
// FILETIME), two WCHAR arrays, and four 32-bit fields. Every field is
// 2- or 4-byte aligned, so Go lays it out as C does.
type rmProcessInfo struct {
	pid              uint32
	startTime        syscall.Filetime
	appName          [cchRmMaxAppName + 1]uint16
	serviceShortName [cchRmMaxSvcName + 1]uint16
	applicationType  uint32
	appStatus        uint32
	tsSessionID      uint32
	restartable      int32
}

// rstrtmgr.dll is not on the KnownDLLs list, so it is loaded by its
// full path in the system directory, never through a search path.
// kernel32 is on the list and resolves from the mapped system copy.
var (
	rstrtmgr                       = syscall.NewLazyDLL(filepath.Join(os.Getenv("SystemRoot"), "System32", "rstrtmgr.dll"))
	procRmStartSession             = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources        = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList                  = rstrtmgr.NewProc("RmGetList")
	procRmEndSession               = rstrtmgr.NewProc("RmEndSession")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

func of(path string) ([]Holder, error) {
	if os.Getenv("SystemRoot") == "" {
		return nil, errors.New("SystemRoot is not set, so rstrtmgr.dll cannot be found")
	}
	if err := rstrtmgr.Load(); err != nil {
		return nil, err
	}
	var session uint32
	var key [cchRmSessionKey + 1]uint16
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil, fmt.Errorf("RmStartSession: %w", syscall.Errno(r))
	}
	defer procRmEndSession.Call(uintptr(session))

	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	names := []*uint16{name}
	if r, _, _ := procRmRegisterResources.Call(uintptr(session),
		uintptr(len(names)), uintptr(unsafe.Pointer(&names[0])), 0, 0, 0, 0); r != 0 {
		return nil, fmt.Errorf("RmRegisterResources: %w", syscall.Errno(r))
	}

	// The list can grow between the call that sizes it and the call
	// that fills it, so the sizing is retried a few times.
	var infos []rmProcessInfo
	for range 5 {
		var needed, reasons uint32
		n := uint32(len(infos))
		var buf uintptr
		if n > 0 {
			buf = uintptr(unsafe.Pointer(&infos[0]))
		}
		r, _, _ := procRmGetList.Call(uintptr(session),
			uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&n)), buf, uintptr(unsafe.Pointer(&reasons)))
		switch errno := syscall.Errno(r); errno {
		case 0:
			return holders(infos[:n]), nil
		case errMoreData:
			infos = make([]rmProcessInfo, needed)
		default:
			return nil, fmt.Errorf("RmGetList: %w", errno)
		}
	}
	return nil, errors.New("RmGetList: the list kept growing")
}

func holders(infos []rmProcessInfo) []Holder {
	out := make([]Holder, 0, len(infos))
	for _, info := range infos {
		out = append(out, Holder{
			PID:     info.pid,
			App:     syscall.UTF16ToString(info.appName[:]),
			Service: syscall.UTF16ToString(info.serviceShortName[:]),
			Image:   imageOf(info.pid),
		})
	}
	return out
}

// imageOf is the program file process pid runs, or empty when the
// system does not say: a process that has exited, or one this test may
// not query.
func imageOf(pid uint32) string {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, syscall.MAX_LONG_PATH)
	size := uint32(len(buf))
	if r, _, _ := procQueryFullProcessImageNameW.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}
