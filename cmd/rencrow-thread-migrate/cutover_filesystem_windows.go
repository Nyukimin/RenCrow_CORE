//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func verifyCutoverSameFilesystem(candidatePath, targetPath string, candidateInfo, targetInfo os.FileInfo) bool {
	if candidateInfo == nil || targetInfo == nil || !candidateInfo.Mode().IsRegular() || !targetInfo.Mode().IsRegular() {
		return false
	}
	candidateCurrent, err := os.Lstat(candidatePath)
	if err != nil || !candidateCurrent.Mode().IsRegular() || !os.SameFile(candidateInfo, candidateCurrent) {
		return false
	}
	targetCurrent, err := os.Lstat(targetPath)
	if err != nil || !targetCurrent.Mode().IsRegular() || !os.SameFile(targetInfo, targetCurrent) {
		return false
	}
	candidateVolume, ok := cutoverWindowsVolumeSerial(candidatePath)
	if !ok {
		return false
	}
	targetVolume, ok := cutoverWindowsVolumeSerial(targetPath)
	if !ok {
		return false
	}
	return candidateVolume == targetVolume
}

func cutoverWindowsVolumeSerial(path string) (uint32, bool) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	handle, err := windows.CreateFile(
		path16,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return 0, false
	}
	var info windows.ByHandleFileInformation
	infoErr := windows.GetFileInformationByHandle(handle, &info)
	closeErr := windows.CloseHandle(handle)
	if infoErr != nil || closeErr != nil {
		return 0, false
	}
	return info.VolumeSerialNumber, true
}
