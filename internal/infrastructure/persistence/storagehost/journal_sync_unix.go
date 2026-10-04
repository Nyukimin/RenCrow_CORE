//go:build linux || darwin

package storagehost

import "os"

// syncDirPlatform makes a directory's entry metadata durable on unix
// platforms: the fsync of the parent directory is what persists the creation
// or rename of a file inside it.
func syncDirPlatform(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
