//go:build windows

package dcimigration

// Windows does not provide a portable directory handle fsync operation. File
// contents are still flushed before rename; the directory durability step is
// intentionally a no-op on this platform.
func syncDirectory(string) error { return nil }
