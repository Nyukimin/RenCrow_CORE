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
		if trimmed == "skill:" || trimmed == "triggers:" || trimmed == "development_contract:" {
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
			case "required_tools":
				manifest.DevelopmentContract.RequiredTools = append(manifest.DevelopmentContract.RequiredTools, item)
			case "required_knowledge":
				manifest.DevelopmentContract.RequiredKnowledge = append(manifest.DevelopmentContract.RequiredKnowledge, item)
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
		case section == "development_contract":
			applyDevelopmentContractField(&manifest.DevelopmentContract, key, value, &listKey)
		default:
			listKey = ""
		}
	}
	return manifest
}

func applyDevelopmentContractField(contract *DevelopmentContract, key, value string, listKey *string) {
	switch key {
	case "required_capability":
		contract.RequiredCapability = value
		*listKey = ""
	case "required_tools":
		if value == "" {
			*listKey = key
			return
		}
		contract.RequiredTools = append(contract.RequiredTools, parseManifestStringList(value)...)
		*listKey = ""
	case "required_knowledge":
		if value == "" {
			*listKey = key
			return
		}
		contract.RequiredKnowledge = append(contract.RequiredKnowledge, parseManifestStringList(value)...)
		*listKey = ""
	case "authority_requirement":
		contract.AuthorityRequirement = value
		*listKey = ""
	case "input_contract":
		contract.InputContract = value
		*listKey = ""
	case "output_contract":
		contract.OutputContract = value
		*listKey = ""
	case "cost_hint":
		contract.CostHint = value
		*listKey = ""
	case "risk_level":
		contract.RiskLevel = value
		*listKey = ""
	case "evaluation_method":
		contract.EvaluationMethod = value
		*listKey = ""
	case "version":
		contract.Version = value
		*listKey = ""
	default:
		*listKey = ""
	}
}

func parseManifestStringList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
		if value == "" {
			return nil
		}
	}
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.Trim(strings.TrimSpace(part), "\"'")
		if item != "" {
			items = append(items, item)
		}
	}
	return items
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
