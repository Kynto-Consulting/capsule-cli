package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kynto/capsule/cli/internal/config"
	"github.com/kynto/capsule/cli/internal/client"
)

var (
	cfg    *config.Config
	apiClient *client.Client
)

var rootCmd = &cobra.Command{
	Use:   "capsule",
	Short: "Capsule — infrastructure, encapsulated",
	Long:  "Capsule CLI — manage your cloud infrastructure from the terminal.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		c, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		cfg = c
		apiURL, _ := cmd.Flags().GetString("api-url")
		if apiURL == "" {
			apiURL = cfg.APIURL
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
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
