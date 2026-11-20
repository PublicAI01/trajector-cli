package fsatomic

import (
	"os"
	"testing"
)

// SwapSyncFile installs f as the flush this package performs before a
// rename, until t ends.
func SwapSyncFile(t *testing.T, f func(*os.File) error) {
	t.Helper()
	swapSyncFile(t, f)
}
