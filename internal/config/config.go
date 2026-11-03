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
	AppsDomain   string `mapstructure:"apps_domain"`
	// CLISessionID names this CLI login for Git credentials (sent as cli_session_id when the
	// session is a JWT, so `capsule logout` can revoke exactly its short-lived tokens).
	CLISessionID string `mapstructure:"cli_session_id"`
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
	// Non-interactive credentials (CI): CAPSULE_* variables win over the file.
	if v := os.Getenv("CAPSULE_TOKEN"); v != "" {
		cfg.Token = v
		cfg.RefreshToken = ""
	}
	if v := os.Getenv("CAPSULE_API_URL"); v != "" {
		cfg.APIURL = v
	}
	if v := os.Getenv("CAPSULE_ORG_ID"); v != "" {
		cfg.OrgID = v
	}
	return &cfg, nil
}

// TokenFromEnv reports whether credentials come from CAPSULE_TOKEN, in which
// case the config file must not be created or rewritten with them.
func TokenFromEnv() bool { return os.Getenv("CAPSULE_TOKEN") != "" }

func Save(cfg *Config) error {
	if TokenFromEnv() {
		return nil // never persist an env-provided token
	}
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
	viper.Set("apps_domain", cfg.AppsDomain)
	viper.Set("cli_session_id", cfg.CLISessionID)

	return viper.WriteConfigAs(filepath.Join(dir, configFile))
}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home dir: %w", err)
	}
	return filepath.Join(home, ".capsule"), nil
}
