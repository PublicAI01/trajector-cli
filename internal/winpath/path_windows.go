package winpath

import (
	"path/filepath"
	"strings"
	"syscall"
)

func Extended(path string) (string, error) {
	path = filepath.FromSlash(path)
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if unc, ok := strings.CutPrefix(abs, `\\`); ok {
		return `\\?\UNC\` + unc, nil
	}
	return `\\?\` + abs, nil
}

func Pointer(path string) (*uint16, error) {
	name, err := Extended(path)
	if err != nil {
		return nil, err
	}
	return syscall.UTF16PtrFromString(name)
}
