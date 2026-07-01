package patch

import (
	"path/filepath"
	"strings"
)

// IsPlaceholderTarget reports whether a patch target still looks like example
// text rather than a concrete repository path.
func IsPlaceholderTarget(target string) bool {
	normalized := strings.TrimSpace(filepath.ToSlash(target))
	if normalized == "" {
		return false
	}
	lower := strings.ToLower(normalized)
	if lower == "path" || lower == "path/to" || strings.HasPrefix(lower, "path/to/") || strings.Contains(lower, "/path/to/") {
		return true
	}
	segments := strings.Split(lower, "/")
	for _, segment := range segments {
		switch segment {
		case "your-file", "your_file", "filename", "file_name", "todo", "placeholder":
			return true
		}
	}
	return strings.Contains(lower, "<") ||
		strings.Contains(lower, ">") ||
		strings.Contains(lower, "{") ||
		strings.Contains(lower, "}") ||
		strings.Contains(lower, "example.com")
}
