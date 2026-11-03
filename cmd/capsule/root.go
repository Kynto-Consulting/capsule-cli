package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
	"github.com/kynto-consulting/capsule/cli/internal/config"
)

var (
	cfg       *config.Config
	apiClient *client.Client
)

// jwtExpired returns true if the JWT access token is expired or unparseable.
func jwtExpired(token string) bool {
	if token == "" {
		return true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return true
	}
	// JWT payload is base64url encoded (no padding)
	payload := parts[1]
	// Add padding if needed
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Also try RawURLEncoding (no padding)
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return true
		}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		return true
	}
	if claims.Exp == 0 {
		return false // no exp claim — assume valid
	}
	return time.Now().Unix() >= claims.Exp
}

// refreshTokenIfNeeded checks whether the stored access token is expired and,
// if so, exchanges the refresh token for a new pair and persists them.
// It updates cfg.Token in-place so the caller can use the fresh token.
func refreshTokenIfNeeded(apiURL string) {
	if !jwtExpired(cfg.Token) {
		return
	}
	if cfg.RefreshToken == "" {
		return
	}

	type refreshReq struct {
		RefreshToken string `json:"refresh_token"`
	}
	type refreshResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}

	b, err := json.Marshal(refreshReq{RefreshToken: cfg.RefreshToken})
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", apiURL+"/api/v1/auth/refresh", bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		_, _ = io.ReadAll(resp.Body)
		return
	}

	var rr refreshResp
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return
	}

	if rr.AccessToken == "" {
		return
	}

	cfg.Token = rr.AccessToken
	if rr.RefreshToken != "" {
		cfg.RefreshToken = rr.RefreshToken
	}
	_ = config.Save(cfg)
}

var rootCmd = &cobra.Command{
	Use:   "capsule",
	Short: "Capsule — infrastructure, encapsulated",
	Long:  "Capsule CLI — manage your cloud infrastructure from the terminal.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Name() == "help" || cmd.Name() == "version" {
			return nil
		}
		// `ci init` only talks to the API with --set-secrets; no config or prompts otherwise.
		if cmd.Name() == "init" && cmd.Parent() != nil && cmd.Parent().Name() == "ci" {
			if ss, _ := cmd.Flags().GetBool("set-secrets"); !ss {
				return nil
			}
		}
		c, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		cfg = c
		apiURL, _ := cmd.Flags().GetString("api-url")
		if apiURL == "" {
			home, _ := os.UserHomeDir()
			confPath := filepath.Join(home, ".capsule", "config.yaml")
			if nonInteractive() || isHelperInvocation(cmd) {
				// No prompts in CI / token mode: the API URL must be explicit.
				if os.Getenv("CAPSULE_API_URL") == "" && cfg.APIURL == "http://localhost:8080" {
					if _, err := os.Stat(confPath); os.IsNotExist(err) {
						if isHelperInvocation(cmd) {
							fmt.Fprintln(os.Stderr, "capsule: not signed in: run `capsule login`")
							return silentErr(codedErr(exitAuth, "not signed in"))
						}
						return fmt.Errorf("API URL not set: pass --api-url or set CAPSULE_API_URL")
					}
				}
			} else if _, err := os.Stat(confPath); os.IsNotExist(err) {
				fmt.Print("Welcome to Capsule CLI! Please configure your Capsule API URL [http://localhost:8080]: ")
				reader := bufio.NewReader(os.Stdin)
				text, _ := reader.ReadString('\n')
				inputURL := strings.TrimSpace(text)
				if inputURL == "" {
					inputURL = "http://localhost:8080"
				}
				cfg.APIURL = inputURL
				_ = config.Save(cfg)
				fmt.Printf("API URL saved to %s\n\n", confPath)
			}
			apiURL = cfg.APIURL
		}
		if !config.TokenFromEnv() {
			refreshTokenIfNeeded(apiURL)
		}
		apiClient = client.New(apiURL, cfg.Token)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().String("api-url", "", "Capsule API URL (overrides config)")
	rootCmd.PersistentFlags().String("output", "table", "Output format: table, json, yaml")
}

func main() {
	rootCmd.SetArgs(expandAliases(os.Args[1:]))
	if err := rootCmd.Execute(); err != nil {
		exitWith(err)
	}
}
