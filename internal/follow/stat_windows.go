//go:build windows

package follow

import "io/fs"

func inodeOf(fs.FileInfo) uint64 { return 0 }
