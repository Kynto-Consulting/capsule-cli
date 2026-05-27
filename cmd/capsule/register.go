package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a new user (with global 2FA onboarding authorization)",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 1. Get Onboarding Status
		var status struct {
			Saved    bool   `json:"saved"`
			Secret   string `json:"secret"`
			QRCode   string `json:"qr_code_uri"`
		}

		if err := apiClient.Get("/api/v1/auth/onboarding/status", &status); err != nil {
			return fmt.Errorf("failed to retrieve onboarding status from backend: %w", err)
		}

		reader := bufio.NewReader(os.Stdin)

		var onboardingCode string

		if !status.Saved {
			fmt.Println("🔒 GLOBAL PLATFORM ONBOARDING REQUIRED")
			fmt.Println("This Capsule instance is not yet initialized. Setting up master platform security...")
			fmt.Println("\n1. SCAN THIS QR CODE OR ADD THE KEY TO YOUR AUTHENTICATOR APP:")
			fmt.Printf("   Secret Key:  %s\n", status.Secret)
			fmt.Printf("   Provision URI: %s\n", status.QRCode)
			fmt.Println("\n2. VERIFICATION:")
			
			for {
				fmt.Print("   Enter current 6-digit Authenticator Code: ")
				codeRaw, _ := reader.ReadString('\n')
				code := strings.TrimSpace(codeRaw)
				if len(code) == 6 {
					onboardingCode = code
					break
				}
				fmt.Println("   ❌ Code must be exactly 6 digits.")
			}
		} else {
			fmt.Println("🔒 SECURE USER REGISTRATION")
			fmt.Println("Registration on this private Capsule server requires the master platform authenticator code.")
			fmt.Print("Enter current 6-digit Global Onboarding Code: ")
			codeRaw, _ := reader.ReadString('\n')
			onboardingCode = strings.TrimSpace(codeRaw)
		}

		// Prompt for user registration details
		fmt.Println("\nENTER NEW USER DETAILS:")
		
		fmt.Print("Full Name: ")
		nameRaw, _ := reader.ReadString('\n')
		name := strings.TrimSpace(nameRaw)

		fmt.Print("Email Address: ")
		emailRaw, _ := reader.ReadString('\n')
		email := strings.TrimSpace(strings.ToLower(emailRaw))

		fmt.Print("Password (min 8 chars): ")
		passwordRaw, _ := reader.ReadString('\n')
		password := strings.TrimSpace(passwordRaw)

		fmt.Print("Invite Code (optional, press Enter if none): ")
		inviteRaw, _ := reader.ReadString('\n')
		inviteCode := strings.TrimSpace(inviteRaw)

		if name == "" || email == "" || password == "" {
			return fmt.Errorf("name, email, and password are required")
		}

		// Register
		var resp struct {
			User   map[string]any `json:"user"`
			Tokens struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"tokens"`
		}

		reqPayload := map[string]string{
			"name":            name,
			"email":           email,
			"password":        password,
			"invite_code":     inviteCode,
			"onboarding_code": onboardingCode,
		}

		if err := apiClient.Post("/api/v1/auth/register", reqPayload, &resp); err != nil {
			return fmt.Errorf("registration failed: %w", err)
		}

		// Save access token
		cfg.Token = resp.Tokens.AccessToken
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}

		fmt.Println("\n✨ Success!")
		if !status.Saved {
			fmt.Printf("Platform Onboarding complete! Registered as platform ADMIN: %s\n", resp.User["email"])
		} else {
			fmt.Printf("Registered new user: %s\n", resp.User["email"])
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(registerCmd)
}
