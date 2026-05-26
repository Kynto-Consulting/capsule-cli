package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Print the authenticated user",
	RunE: func(cmd *cobra.Command, args []string) error {
		var user map[string]any
		if err := apiClient.Get("/api/v1/auth/me", &user); err != nil {
			return err
		}

		output, _ := cmd.Flags().GetString("output")
		if output == "json" {
			b, _ := json.MarshalIndent(user, "", "  ")
			fmt.Println(string(b))
			return nil
		}

		fmt.Printf("ID:    %v\n", user["id"])
		fmt.Printf("Name:  %v\n", user["name"])
		fmt.Printf("Email: %v\n", user["email"])
		fmt.Printf("Role:  %v\n", user["role"])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}
