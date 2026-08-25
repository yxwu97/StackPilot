//go:build windows

package workspace

import (
	"os"

	"golang.org/x/sys/windows"
)

func atomicReplaceManifest(from, to string, replace bool) error {
	if !replace {
		return os.Rename(from, to)
	}
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
