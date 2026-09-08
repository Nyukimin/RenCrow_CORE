//go:build windows

package jsonlbatch

// Windows has no portable directory-handle durability contract for this
// boundary. New instead requires Sync on every created/opened file; on native
// Windows this maps to FlushFileBuffers for the file handle and its metadata.
// Keep this no-op so the helper does not claim an unverified directory flush.
// Native Windows power-loss behavior remains an unverified deployment check.
func syncDirectory(_ string) error { return nil }
