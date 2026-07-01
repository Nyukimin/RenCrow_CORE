package persona

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Registry struct {
	Personas map[string]Definition
}

type Definition struct {
	Name           string   `yaml:"name"`
	Role           string   `yaml:"role"`
	ModelAlias     string   `yaml:"model_alias"`
	Goals          []string `yaml:"goals"`
	StyleGuidePath string   `yaml:"styleguide"`
	StyleGuideText string   `yaml:"-"`
}

func LoadRegistry(root string) (*Registry, error) {
	personaDir := filepath.Join(root, "personas")
	files, err := filepath.Glob(filepath.Join(personaDir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	registry := &Registry{Personas: map[string]Definition{}}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var def Definition
		if err := yaml.Unmarshal(data, &def); err != nil {
			return nil, err
		}
		if strings.TrimSpace(def.Name) == "" {
			def.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		if strings.TrimSpace(def.StyleGuidePath) != "" {
			guidePath := def.StyleGuidePath
			if !filepath.IsAbs(guidePath) {
				guidePath = filepath.Join(filepath.Dir(path), guidePath)
			}
			guide, err := os.ReadFile(filepath.Clean(guidePath))
			if err != nil {
				return nil, err
			}
			def.StyleGuideText = strings.TrimSpace(string(guide))
		}
		registry.Personas[def.Name] = def
	}
	return registry, nil
}
