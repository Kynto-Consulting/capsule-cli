package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kynto/capsule/cli/internal/config"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Capsule",
	RunE: func(cmd *cobra.Command, args []string) error {
		email, _ := cmd.Flags().GetString("email")
		password, _ := cmd.Flags().GetString("password")

		if email == "" || password == "" {
			return fmt.Errorf("--email and --password are required")
		}

		var resp struct {
			User   map[string]any `json:"user"`
			Tokens struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"tokens"`
		}

		if err := apiClient.Post("/api/v1/auth/login", map[string]string{
			"email":    email,
			"password": password,
		}, &resp); err != nil {
			return fmt.Errorf("login failed: %w", err)
		}

		cfg.Token = resp.Tokens.AccessToken
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}

		fmt.Printf("Logged in as %v\n", resp.User["email"])
		return nil
	},
}

func init() {
	loginCmd.Flags().String("email", "", "Email address")
	loginCmd.Flags().String("password", "", "Password")
	rootCmd.AddCommand(loginCmd)
}
