package promptbundle

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

const Separator = "\n\n---\n\n"

// Bundle is a manifest-based prompt bundle loaded from disk.
type Bundle struct {
	Name    string
	Root    string
	Dir     string
	Content string
}

// ReadFile reads a non-empty prompt file relative to baseDir.
func ReadFile(baseDir, relPath string) (string, bool) {
	path := filepath.Join(baseDir, relPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", false
	}
	return content, true
}

// ReadBundle reads manifest.txt from dir and joins listed markdown files.
func ReadBundle(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.txt"))
	if err != nil {
		return "", false
	}
	var parts []string
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "..") || strings.ContainsAny(line, `/\`) || filepath.Ext(line) != ".md" {
			log.Printf("WARN: ignoring invalid prompt bundle entry dir=%s entry=%q", dir, line)
			continue
		}
		content, ok := ReadFile(dir, line)
		if !ok {
			log.Printf("WARN: prompt bundle entry missing or empty dir=%s entry=%q", dir, line)
			continue
		}
		parts = append(parts, content)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, Separator), true
}

// LoadCharacterBundlesFromDir loads character bundles from supported roots.
func LoadCharacterBundlesFromDir(dir string) []Bundle {
	var bundles []Bundle
	for _, root := range CharacterRoots(dir) {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		loaded := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(entry.Name()))
			if name == "" {
				continue
			}
			bundleDir := filepath.Join(root, name)
			content, ok := ReadBundle(bundleDir)
			if !ok {
				continue
			}
			bundles = append(bundles, Bundle{
				Name:    name,
				Root:    root,
				Dir:     bundleDir,
				Content: content,
			})
			loaded++
		}
		if loaded > 0 {
			log.Printf("Loaded %d character prompt bundles from %s", loaded, root)
		}
	}
	return bundles
}

// CharacterRoots returns character bundle roots in override order.
func CharacterRoots(dir string) []string {
	if dir == "" {
		return nil
	}
	candidates := []string{
		filepath.Join(dir, "characters"),
		filepath.Join(dir, "prompts", "characters"),
	}
	roots := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		roots = append(roots, clean)
	}
	return roots
}
