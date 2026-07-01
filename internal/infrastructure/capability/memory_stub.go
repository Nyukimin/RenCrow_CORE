//go:build !linux

package capability

// readMemoryInfo は非 Linux 環境ではメモリ情報を返せないためゼロを返す。
// Windows では GlobalMemoryStatusEx、Darwin では vm_stat 等で実装可能（将来対応）。
func readMemoryInfo() (totalMB, availableMB uint64) {
	return 0, 0
}
