package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const projectConfigFile = ".capsule.json"

type ProjectConfig struct {
	OrgID       string `json:"org_id"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	OrgName     string `json:"org_name"`
}

// FindProjectConfig walks up from dir looking for .capsule.json
func FindProjectConfig(dir string) (*ProjectConfig, string, error) {
	current := dir
	for {
		path := filepath.Join(current, projectConfigFile)
		if data, err := os.ReadFile(path); err == nil {
			var pc ProjectConfig
			if err := json.Unmarshal(data, &pc); err == nil {
				return &pc, path, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil, "", os.ErrNotExist
}

// SaveProjectConfig writes .capsule.json in the given dir
func SaveProjectConfig(dir string, pc *ProjectConfig) error {
	data, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, projectConfigFile), data, 0644)
}
