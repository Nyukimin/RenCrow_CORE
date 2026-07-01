package skillgovernance

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func LoadManifestsFromDirs(skillRoots ...string) ([]SkillManifest, error) {
	var manifests []SkillManifest
	seen := map[string]bool{}
	for _, root := range skillRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || entry.Name() != "skill_manifest.yaml" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			manifest := ParseManifestYAML(string(data))
			if manifest.SkillID == "" {
				return nil
			}
			if seen[manifest.SkillID] {
				return nil
			}
			manifest.Path = filepath.Dir(path)
			if manifest.Scope == "" {
				manifest.Scope = inferScope(root, path)
			}
			if manifest.Version == "" {
				manifest.Version = "0.0.0"
			}
			if manifest.UpdatedAt.IsZero() {
				manifest.UpdatedAt = time.Now().UTC()
			}
			seen[manifest.SkillID] = true
			manifests = append(manifests, manifest)
			return nil
		})
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
	}
	return manifests, nil
}

func ParseManifestYAML(content string) SkillManifest {
	manifest := SkillManifest{Enabled: true}
	var section string
	var listKey string
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "skill:" || trimmed == "triggers:" {
			section = strings.TrimSuffix(trimmed, ":")
			listKey = ""
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			item := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), "\"'")
			switch listKey {
			case "keywords":
				manifest.KeywordTriggers = append(manifest.KeywordTriggers, item)
			case "intents":
				manifest.IntentTriggers = append(manifest.IntentTriggers, item)
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			listKey = ""
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch {
		case section == "skill":
			applySkillField(&manifest, key, value)
		case section == "triggers":
			if value == "" && (key == "keywords" || key == "intents") {
				listKey = key
			} else {
				listKey = ""
			}
		default:
			if key == "human_approval_required" {
				manifest.HumanApprovalRequired = value == "true"
			}
			listKey = ""
		}
	}
	return manifest
}

func applySkillField(manifest *SkillManifest, key, value string) {
	switch key {
	case "id":
		manifest.SkillID = value
	case "name":
		manifest.Name = value
	case "scope":
		manifest.Scope = value
	case "version":
		manifest.Version = value
	case "description":
		manifest.Description = value
	case "human_approval_required":
		manifest.HumanApprovalRequired = value == "true"
	case "enabled":
		manifest.Enabled = value != "false"
	}
}

func inferScope(root, manifestPath string) string {
	rel, err := filepath.Rel(root, manifestPath)
	if err != nil {
		return ScopeProject
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 0 {
		switch parts[0] {
		case ScopeCore:
			return ScopeCore
		case ScopePlugin:
			return ScopePlugin
		case "plugins":
			return ScopePlugin
		case ScopeProject:
			return ScopeProject
		case "projects":
			return ScopeProject
		}
	}
	return ScopeProject
}
