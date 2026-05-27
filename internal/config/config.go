package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

const (
	defaultAPIURL = "http://localhost:8080"
	configFile    = "config.yaml"
)

type Config struct {
	APIURL       string `mapstructure:"api_url"`
	Token        string `mapstructure:"token"`
	RefreshToken string `mapstructure:"refresh_token"`
	OrgID        string `mapstructure:"org_id"`
}

func Load() (*Config, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}

	viper.SetConfigFile(filepath.Join(dir, configFile))
	viper.SetDefault("api_url", defaultAPIURL)
	viper.AutomaticEnv()

	_ = viper.ReadInConfig()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	viper.Set("api_url", cfg.APIURL)
	viper.Set("token", cfg.Token)
	viper.Set("refresh_token", cfg.RefreshToken)
	viper.Set("org_id", cfg.OrgID)

	return viper.WriteConfigAs(filepath.Join(dir, configFile))
}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home dir: %w", err)
	}
	return filepath.Join(home, ".capsule"), nil
}
