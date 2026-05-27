package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const projectConfigFile = ".capsule.json"

type ProjectConfig struct {
	OrgID       string `json:"org_id"`
	OrgName     string `json:"org_name"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`

	// Deploy settings
	DeployType     string `json:"deploy_type,omitempty"`     // "docker" | "lambda" | "static"
	Port           int    `json:"port,omitempty"`            // container port
	BuildCommand   string `json:"build_command,omitempty"`
	StartCommand   string `json:"start_command,omitempty"`
	OutputDir      string `json:"output_dir,omitempty"`      // for static sites
	InstallCommand string `json:"install_command,omitempty"`
	NodeVersion    string `json:"node_version,omitempty"`
	GoVersion      string `json:"go_version,omitempty"`
	PythonVersion  string `json:"python_version,omitempty"`
	EnvFile        string `json:"env_file,omitempty"`
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
