package persona

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

func LoadCharacters(root string) (map[string]domainpersona.CharacterProfile, error) {
	charactersRoot := filepath.Join(root, "characters")
	entries, err := os.ReadDir(charactersRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]domainpersona.CharacterProfile{}, nil
		}
		return nil, err
	}
	out := map[string]domainpersona.CharacterProfile{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		characterID := strings.TrimSpace(entry.Name())
		if characterID == "" {
			continue
		}
		characterRoot := filepath.Join(charactersRoot, characterID)
		lore, err := readCharacterSection(filepath.Join(characterRoot, "lore"))
		if err != nil {
			return nil, err
		}
		persona, err := readCharacterSection(filepath.Join(characterRoot, "persona"))
		if err != nil {
			return nil, err
		}
		modes, err := readCharacterSection(filepath.Join(characterRoot, "modes"))
		if err != nil {
			return nil, err
		}
		out[characterID] = domainpersona.CharacterProfile{
			CharacterID: characterID,
			Lore:        lore,
			Persona:     persona,
			Modes:       modes,
		}
	}
	return out, nil
}

func readCharacterSection(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		out[key] = strings.TrimSpace(string(data))
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}
