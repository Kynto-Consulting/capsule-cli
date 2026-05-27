package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// ── API types ─────────────────────────────────────────────────────────────────

type envVarItem struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	IsSecret  bool   `json:"is_secret"`
	Scope     string `json:"scope"`
}

// ── helpers ───────────────────────────────────────────────────────────────────

func envPath(orgID, projectID string) string {
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s/env", orgID, projectID)
}

func maskValue(v envVarItem) string {
	if v.IsSecret {
		return "***"
	}
	return v.Value
}

func secretLabel(isSecret bool) string {
	if isSecret {
		return "yes"
	}
	return "no"
}

// fetchEnvVars retrieves all env vars for a project.
func fetchEnvVars(orgID, projectID string) ([]envVarItem, error) {
	var vars []envVarItem
	if err := apiClient.Get(envPath(orgID, projectID), &vars); err != nil {
		return nil, fmt.Errorf("fetching env vars: %w", err)
	}
	return vars, nil
}

// ── command group ─────────────────────────────────────────────────────────────

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage environment variables for a project",
}

// ── env list ──────────────────────────────────────────────────────────────────

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List environment variables",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		vars, err := fetchEnvVars(orgID, projectID)
		if err != nil {
			return err
		}

		if len(vars) == 0 {
			fmt.Println("No environment variables found. Set one with: capsule env set KEY=VALUE")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "KEY\tVALUE\tSCOPE\tSECRET")
		for _, v := range vars {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Key, maskValue(v), v.Scope, secretLabel(v.IsSecret))
		}
		return w.Flush()
	},
}

// ── env set ───────────────────────────────────────────────────────────────────

var envSetCmd = &cobra.Command{
	Use:   "set KEY=VALUE",
	Short: "Create or update an environment variable",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		parts := strings.SplitN(args[0], "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("argument must be in KEY=VALUE format")
		}
		key := strings.TrimSpace(parts[0])
		value := parts[1]
		if key == "" {
			return fmt.Errorf("key must not be empty")
		}

		isSecret, _ := cmd.Flags().GetBool("secret")
		scope, _ := cmd.Flags().GetString("scope")

		body := map[string]interface{}{
			"key":       key,
			"value":     value,
			"is_secret": isSecret,
			"scope":     scope,
		}

		var result envVarItem
		if err := apiClient.Put(envPath(orgID, projectID), body, &result); err != nil {
			return fmt.Errorf("setting env var: %w", err)
		}

		fmt.Printf("Set %s (%s)\n", result.Key, result.Scope)
		return nil
	},
}

// ── env get ───────────────────────────────────────────────────────────────────

var envGetCmd = &cobra.Command{
	Use:   "get KEY",
	Short: "Show the value of an environment variable",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		key := strings.ToUpper(strings.TrimSpace(args[0]))

		vars, err := fetchEnvVars(orgID, projectID)
		if err != nil {
			return err
		}

		for _, v := range vars {
			if v.Key == key {
				fmt.Println(v.Value)
				return nil
			}
		}

		return fmt.Errorf("env var %q not found", key)
	},
}

// ── env delete ────────────────────────────────────────────────────────────────

var envDeleteCmd = &cobra.Command{
	Use:   "delete KEY",
	Short: "Delete an environment variable",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		key := strings.ToUpper(strings.TrimSpace(args[0]))
		path := fmt.Sprintf("%s/%s", envPath(orgID, projectID), key)

		if err := apiClient.Delete(path); err != nil {
			return fmt.Errorf("deleting env var: %w", err)
		}

		fmt.Printf("Deleted %s\n", key)
		return nil
	},
}

// ── env pull ──────────────────────────────────────────────────────────────────

var envPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Download environment variables to a .env file",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		output, _ := cmd.Flags().GetString("output")

		vars, err := fetchEnvVars(orgID, projectID)
		if err != nil {
			return err
		}

		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()

		bw := bufio.NewWriter(f)
		for _, v := range vars {
			val := maskValue(v)
			if _, err := fmt.Fprintf(bw, "%s=%s\n", v.Key, val); err != nil {
				return fmt.Errorf("writing env file: %w", err)
			}
		}
		if err := bw.Flush(); err != nil {
			return fmt.Errorf("flushing env file: %w", err)
		}

		fmt.Printf("Wrote %d variable(s) to %s\n", len(vars), output)
		return nil
	},
}

// ── env push ──────────────────────────────────────────────────────────────────

var envPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Upload environment variables from a .env file",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		input, _ := cmd.Flags().GetString("input")
		overwrite, _ := cmd.Flags().GetBool("overwrite")

		f, err := os.Open(input)
		if err != nil {
			return fmt.Errorf("opening input file: %w", err)
		}
		defer f.Close()

		// If not overwriting, fetch existing keys to skip them.
		existing := map[string]bool{}
		if !overwrite {
			vars, err := fetchEnvVars(orgID, projectID)
			if err != nil {
				return err
			}
			for _, v := range vars {
				existing[v.Key] = true
			}
		}

		scanner := bufio.NewScanner(f)
		pushed := 0
		skipped := 0
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			// Skip blank lines and comments.
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			value := parts[1]
			if key == "" {
				continue
			}

			upperKey := strings.ToUpper(key)
			if !overwrite && existing[upperKey] {
				skipped++
				continue
			}

			body := map[string]interface{}{
				"key":       upperKey,
				"value":     value,
				"is_secret": false,
				"scope":     "runtime",
			}
			var result envVarItem
			if err := apiClient.Put(envPath(orgID, projectID), body, &result); err != nil {
				return fmt.Errorf("setting %s: %w", upperKey, err)
			}
			pushed++
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("reading input file: %w", err)
		}

		fmt.Printf("Pushed %d variable(s)", pushed)
		if skipped > 0 {
			fmt.Printf(", skipped %d existing (use --overwrite to replace)", skipped)
		}
		fmt.Println()
		return nil
	},
}

// ── init ──────────────────────────────────────────────────────────────────────

func init() {
	// Shared org/project flags for all subcommands.
	for _, sub := range []*cobra.Command{envListCmd, envSetCmd, envGetCmd, envDeleteCmd, envPullCmd, envPushCmd} {
		sub.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		sub.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	}

	// env set flags
	envSetCmd.Flags().Bool("secret", false, "Mark value as a secret (masked in list output)")
	envSetCmd.Flags().String("scope", "runtime", "Scope: build | runtime | both")

	// env pull flags
	envPullCmd.Flags().String("output", ".env", "Output file path")

	// env push flags
	envPushCmd.Flags().String("input", ".env", "Input .env file path")
	envPushCmd.Flags().Bool("overwrite", false, "Overwrite existing variables")

	envCmd.AddCommand(envListCmd)
	envCmd.AddCommand(envSetCmd)
	envCmd.AddCommand(envGetCmd)
	envCmd.AddCommand(envDeleteCmd)
	envCmd.AddCommand(envPullCmd)
	envCmd.AddCommand(envPushCmd)

	rootCmd.AddCommand(envCmd)
}
