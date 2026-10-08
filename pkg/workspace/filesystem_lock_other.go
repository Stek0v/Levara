//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package workspace

import (
	"fmt"
	"os"
)

func workspaceTryLock(*os.File) (bool, error) {
	return false, fmt.Errorf("workspace process locking unsupported on this platform")
}
func workspaceUnlock(*os.File) {}

func workspaceLinkCount(*os.File) (uint64, error) {
	return 0, fmt.Errorf("workspace file verification unsupported on this platform")
}
func workspaceNativeOpen(*os.Root, string, int, os.FileMode) (*os.File, error) {
	return nil, fmt.Errorf("workspace confined read unsupported on this platform")
}
