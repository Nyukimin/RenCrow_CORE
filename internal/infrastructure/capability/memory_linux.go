//go:build linux

package capability

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// readMemoryInfo は /proc/meminfo からメモリ情報を読み取る（Linux 専用）
func readMemoryInfo() (totalMB, availableMB uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	var totalKB, availKB uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB = val
		case "MemAvailable:":
			availKB = val
		}
	}
	return totalKB / 1024, availKB / 1024
}
