package tts

import (
	"path"
	"path/filepath"
	"strings"
)

// resolveAudioPath maps relative audio_path from TTS server into local playable path.
func resolveAudioPath(audioPath string, root string) string {
	audioPath = strings.TrimSpace(audioPath)
	if audioPath == "" {
		return ""
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return audioPath
	}

	slashPath := strings.ReplaceAll(audioPath, "\\", "/")
	if path.IsAbs(slashPath) || isWindowsAbsPath(audioPath) {
		return audioPath
	}

	slashPath = strings.TrimPrefix(slashPath, "./")
	slashPath = strings.TrimPrefix(slashPath, "/")
	return filepath.Clean(filepath.Join(root, filepath.FromSlash(slashPath)))
}

func isWindowsAbsPath(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) < 3 {
		return false
	}
	drive := p[0]
	if !((drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')) {
		return false
	}
	if p[1] != ':' {
		return false
	}
	return p[2] == '\\' || p[2] == '/'
}
