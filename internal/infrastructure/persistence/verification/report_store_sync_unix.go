//go:build linux || darwin

package verification

import "os"

func syncVerificationReportDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
