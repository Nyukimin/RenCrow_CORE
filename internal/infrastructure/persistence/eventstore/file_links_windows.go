//go:build windows

package eventstore

import (
	"os"

	"golang.org/x/sys/windows"
)

func eventStoreFileHasMultipleLinks(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return false, err
	} else if !info.Mode().IsRegular() {
		return false, nil
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return false, err
	}
	return info.NumberOfLinks > 1, nil
}
