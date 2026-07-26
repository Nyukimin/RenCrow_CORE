package aiworkflow

import (
	"path/filepath"
	"strings"
)

// isCallerAbsPath reports whether a caller-supplied path is absolute on any
// supported platform.
//
// filepath.IsAbs only recognizes the host convention: on Windows "/tmp/x" is
// reported as relative, and on Unix "C:\tmp\x" is. Path policies here must
// refuse such input instead of silently joining it under base_dir, so both
// forms are treated as absolute regardless of the host OS.
func isCallerAbsPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return true
	}
	slash := filepath.ToSlash(p)
	if strings.HasPrefix(slash, "/") {
		return true
	}
	if len(slash) >= 3 && slash[1] == ':' && slash[2] == '/' {
		drive := slash[0]
		return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
	}
	return false
}
