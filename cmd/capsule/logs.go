package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View project runtime, lambda, worker, and storage logs",
}

// logsRuntimeCmd shows docker container stdout/stderr for the deployed app.
var logsRuntimeCmd = &cobra.Command{
	Use:   "runtime",
	Short: "Show runtime container logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		tail, _ := cmd.Flags().GetInt("tail")
		follow, _ := cmd.Flags().GetBool("follow")

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/logs/runtime?tail=%d", orgID, projectID, tail)

		printLogLines := func() error {
			var resp struct {
				Container string   `json:"container"`
				Lines     []string `json:"lines"`
			}
			if err := apiClient.Get(path, &resp); err != nil {
				return err
			}
			for _, line := range resp.Lines {
				fmt.Println(formatLogLine(line))
			}
			return nil
		}

		if err := printLogLines(); err != nil {
			return err
		}
		if follow {
			for {
				time.Sleep(2 * time.Second)
				if err := printLogLines(); err != nil {
					return err
				}
			}
		}
		return nil
	},
}

// logsLambdaCmd shows lambda execution logs stored in the DB.
var logsLambdaCmd = &cobra.Command{
	Use:   "lambda",
	Short: "Show lambda execution logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		tail, _ := cmd.Flags().GetInt("tail")
		follow, _ := cmd.Flags().GetBool("follow")

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/logs/lambda?tail=%d", orgID, projectID, tail)

		printLogLines := func() error {
			var resp struct {
				Data []struct {
					Level     string    `json:"level"`
					Message   string    `json:"message"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"data"`
			}
			if err := apiClient.Get(path, &resp); err != nil {
				return err
			}
			for _, l := range resp.Data {
				fmt.Printf("[%s] %-6s %s\n",
					l.CreatedAt.Format("15:04:05"),
					l.Level,
					l.Message,
				)
			}
			return nil
		}

		if err := printLogLines(); err != nil {
			return err
		}
		if follow {
			for {
				time.Sleep(2 * time.Second)
				if err := printLogLines(); err != nil {
					return err
				}
			}
		}
		return nil
	},
}

// logsWorkersCmd shows docker logs for a specific worker container.
var logsWorkersCmd = &cobra.Command{
	Use:   "workers <worker-id>",
	Short: "Show worker container logs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		tail, _ := cmd.Flags().GetInt("tail")
		follow, _ := cmd.Flags().GetBool("follow")
		workerID := args[0]

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/logs/workers/%s?tail=%d", orgID, projectID, workerID, tail)

		printLogLines := func() error {
			var resp struct {
				Container string   `json:"container"`
				Lines     []string `json:"lines"`
			}
			if err := apiClient.Get(path, &resp); err != nil {
				return err
			}
			for _, line := range resp.Lines {
				fmt.Println(formatLogLine(line))
			}
			return nil
		}

		if err := printLogLines(); err != nil {
			return err
		}
		if follow {
			for {
				time.Sleep(2 * time.Second)
				if err := printLogLines(); err != nil {
					return err
				}
			}
		}
		return nil
	},
}

// logsStorageCmd shows storage access logs stored in the DB.
var logsStorageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Show storage access logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		follow, _ := cmd.Flags().GetBool("follow")

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/logs/storage", orgID, projectID)

		printLogLines := func() error {
			var resp struct {
				Data []struct {
					Level     string    `json:"level"`
					Message   string    `json:"message"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"data"`
			}
			if err := apiClient.Get(path, &resp); err != nil {
				return err
			}
			for _, l := range resp.Data {
				fmt.Printf("[%s] %-6s %s\n",
					l.CreatedAt.Format("15:04:05"),
					l.Level,
					l.Message,
				)
			}
			return nil
		}

		if err := printLogLines(); err != nil {
			return err
		}
		if follow {
			for {
				time.Sleep(2 * time.Second)
				if err := printLogLines(); err != nil {
					return err
				}
			}
		}
		return nil
	},
}

// formatLogLine returns a log line with a timestamp prefix.
func formatLogLine(line string) string {
	return fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), line)
}

func init() {
	// Add --org / --project / --tail / --follow to all subcommands
	for _, c := range []*cobra.Command{
		logsRuntimeCmd, logsLambdaCmd, logsWorkersCmd, logsStorageCmd,
	} {
		c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
		c.Flags().Bool("follow", false, "Poll for new log lines every 2 seconds")
	}

	logsRuntimeCmd.Flags().Int("tail", 200, "Number of lines to fetch (max 1000)")
	logsLambdaCmd.Flags().Int("tail", 100, "Number of log entries to fetch (max 1000)")
	logsWorkersCmd.Flags().Int("tail", 200, "Number of lines to fetch (max 1000)")

	logsCmd.AddCommand(logsRuntimeCmd, logsLambdaCmd, logsWorkersCmd, logsStorageCmd)
	rootCmd.AddCommand(logsCmd)
}
