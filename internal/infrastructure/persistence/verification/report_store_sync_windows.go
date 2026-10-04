//go:build windows

package verification

// The operation/report files are made durable only by their successful Go
// File.Sync calls. Windows has no additional parent-directory barrier here;
// durability of ancestor-directory creation and non-NTFS filesystems is not
// established by this helper.
func syncVerificationReportDirectory(string) error { return nil }
