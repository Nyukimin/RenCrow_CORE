//go:build !windows

package main

import (
	"os"
	"syscall"
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
	candidateStat, ok := candidateCurrent.Sys().(*syscall.Stat_t)
	if !ok || candidateStat == nil {
		return false
	}
	targetStat, ok := targetCurrent.Sys().(*syscall.Stat_t)
	if !ok || targetStat == nil {
		return false
	}
	return candidateStat.Dev == targetStat.Dev
}
