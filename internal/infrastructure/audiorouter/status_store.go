package audiorouter

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type StatusStore struct {
	path string
}

func NewStatusStore(path string) *StatusStore {
	return &StatusStore{path: path}
}

func (s *StatusStore) Write(status Status) error {
	if s == nil || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *StatusStore) Read() (Status, error) {
	var status Status
	if s == nil || s.path == "" {
		return status, os.ErrNotExist
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, err
	}
	if status.Characters == nil {
		status.Characters = map[string]CharacterStatus{}
	}
	return status, nil
}
