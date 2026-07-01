package tts

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const LocalAudioViewerPath = "/viewer/tts/audio"

func BuildLocalAudioURL(outputDir, audioPath string) string {
	rel, ok := LocalAudioRelPath(outputDir, audioPath)
	if !ok {
		return ""
	}
	return LocalAudioViewerPath + "?path=" + url.QueryEscape(rel)
}

func LocalAudioRelPath(outputDir, audioPath string) (string, bool) {
	baseDir, ok := NormalizeLocalAudioBase(outputDir)
	if !ok {
		return "", false
	}
	audioPath = strings.TrimSpace(audioPath)
	if audioPath == "" {
		return "", false
	}

	candidate := audioPath
	if !filepath.IsAbs(candidate) {
		if absCandidate, err := filepath.Abs(filepath.FromSlash(candidate)); err == nil && isPathWithinBase(baseDir, absCandidate) {
			candidate = absCandidate
		} else {
			candidate = filepath.Join(baseDir, filepath.FromSlash(candidate))
		}
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}

	rel, err := filepath.Rel(baseDir, candidate)
	if err != nil {
		return "", false
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func isPathWithinBase(baseDir, candidate string) bool {
	rel, err := filepath.Rel(baseDir, candidate)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func NormalizeLocalAudioBase(outputDir string) (string, bool) {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return "", false
	}
	absDir, err := filepath.Abs(outputDir)
	if err != nil {
		return "", false
	}
	return filepath.Clean(absDir), true
}

func ResolveLocalAudioPath(baseDir, rawRelPath string) (string, bool) {
	rawRelPath = strings.TrimSpace(rawRelPath)
	if rawRelPath == "" {
		return "", false
	}
	rawRelPath = filepath.Clean(filepath.FromSlash(rawRelPath))
	if rawRelPath == "." || rawRelPath == ".." || filepath.IsAbs(rawRelPath) {
		return "", false
	}
	if strings.HasPrefix(rawRelPath, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.Join(baseDir, rawRelPath), true
}
