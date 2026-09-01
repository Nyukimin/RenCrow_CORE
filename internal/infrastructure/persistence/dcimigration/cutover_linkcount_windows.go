//go:build windows

package dcimigration

import (
	"os"

	"golang.org/x/sys/windows"
)

// cutoverKnownFileIsUnaliased verifies the exact Windows file identity and
// link count through a handle opened with attribute-only access.  All sharing
// modes remain enabled so a stopped-or-running owner cannot be blocked merely
// by an unrelated compatible handle.
func cutoverKnownFileIsUnaliased(path string, info os.FileInfo) bool {
	if info == nil {
		return false
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return false
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var first, second windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &first); err != nil || first.NumberOfLinks != 1 {
		return false
	}
	if err := windows.GetFileInformationByHandle(handle, &second); err != nil || second.NumberOfLinks != 1 {
		return false
	}
	if first.VolumeSerialNumber != second.VolumeSerialNumber || first.FileIndexHigh != second.FileIndexHigh || first.FileIndexLow != second.FileIndexLow {
		return false
	}
	latest, err := os.Lstat(path)
	return err == nil && os.SameFile(info, latest)
}
