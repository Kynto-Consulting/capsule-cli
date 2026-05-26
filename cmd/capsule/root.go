package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		if cmd.Name() == "help" || cmd.Name() == "version" {
			return nil
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
			if _, err := os.Stat(confPath); os.IsNotExist(err) {
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
