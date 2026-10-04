//go:build windows

package storagehost

// syncDirPlatform is the platform-defined directory durability operation.
// NTFS writes file and directory metadata through to the log before the
// creating call returns, so there is no separate directory fsync; an attempt
// to FlushFileBuffers a directory handle fails with ACCESS_DENIED. The
// platform guarantees are documented here and the operation is a no-op, not
// a silent local fallback.
func syncDirPlatform(path string) error {
	return nil
}
