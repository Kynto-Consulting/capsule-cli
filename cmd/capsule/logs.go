package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View project logs: runtime, build, lambda, storage, cron, workers",
}

// logsRuntimeCmd streams docker container stdout/stderr.
var logsRuntimeCmd = &cobra.Command{
	Use:   "runtime",
	Short: "Stream docker container logs (docker 24/7 projects)",
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

// logsBuildCmd shows build logs for a deployment.
var logsBuildCmd = &cobra.Command{
	Use:   "build [deployment-id]",
	Short: "Show build/deploy logs (latest deployment if no ID given)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		follow, _ := cmd.Flags().GetBool("follow")

		// If no deployment ID given, fetch latest
		deploymentID := ""
		if len(args) > 0 {
			deploymentID = args[0]
		} else {
			var depsResp struct {
				Data []struct {
					ID      string `json:"id"`
					Version string `json:"version"`
					Status  string `json:"status"`
				} `json:"data"`
			}
			depsPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments", orgID, projectID)
			if err := apiClient.Get(depsPath, &depsResp); err != nil {
				return fmt.Errorf("fetching deployments: %w", err)
			}
			if len(depsResp.Data) == 0 {
				return fmt.Errorf("no deployments found for this project")
			}
			deploymentID = depsResp.Data[0].ID
			fmt.Printf("Using latest deployment: %s\n\n", deploymentID)
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/%s/logs", orgID, projectID, deploymentID)

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
				ts := l.CreatedAt.Format("15:04:05")
				color := levelColor(l.Level)
				fmt.Printf("[%s] %s%-5s\033[0m %s\n", ts, color, l.Level, l.Message)
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
	Short: "Show serverless/lambda execution logs",
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
					SourceID  string    `json:"source_id"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"data"`
			}
			if err := apiClient.Get(path, &resp); err != nil {
				return err
			}
			for _, l := range resp.Data {
				color := levelColor(l.Level)
				fmt.Printf("[%s] %s%-5s\033[0m %s\n",
					l.CreatedAt.Format("15:04:05"),
					color,
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

// logsStorageCmd shows S3/DB storage access logs.
var logsStorageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Show storage (S3/database) access logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		tail, _ := cmd.Flags().GetInt("tail")
		follow, _ := cmd.Flags().GetBool("follow")

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/logs/storage?tail=%d", orgID, projectID, tail)

		printLogLines := func() error {
			var resp struct {
				Data []struct {
					Level     string    `json:"level"`
					Message   string    `json:"message"`
					SourceID  string    `json:"source_id"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"data"`
			}
			if err := apiClient.Get(path, &resp); err != nil {
				return err
			}
			for _, l := range resp.Data {
				color := levelColor(l.Level)
				fmt.Printf("[%s] %s%-5s\033[0m %-30s %s\n",
					l.CreatedAt.Format("15:04:05"),
					color,
					l.Level,
					l.SourceID,
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

// logsCronCmd shows cron job execution logs.
var logsCronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Show cron job execution logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		tail, _ := cmd.Flags().GetInt("tail")
		follow, _ := cmd.Flags().GetBool("follow")

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/logs/cron?tail=%d", orgID, projectID, tail)

		printLogLines := func() error {
			var resp struct {
				Data []struct {
					Level     string    `json:"level"`
					Message   string    `json:"message"`
					SourceID  string    `json:"source_id"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"data"`
			}
			if err := apiClient.Get(path, &resp); err != nil {
				return err
			}
			for _, l := range resp.Data {
				color := levelColor(l.Level)
				fmt.Printf("[%s] %s%-5s\033[0m %s\n",
					l.CreatedAt.Format("15:04:05"),
					color,
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

// levelColor returns an ANSI color code for the log level.
func levelColor(level string) string {
	switch level {
	case "error":
		return "\033[31m" // red
	case "warn":
		return "\033[33m" // yellow
	case "info":
		return "\033[36m" // cyan
	default:
		return "\033[0m"
	}
}

func init() {
	// Shared flags for all subcommands
	for _, c := range []*cobra.Command{
		logsRuntimeCmd, logsBuildCmd, logsLambdaCmd, logsWorkersCmd, logsStorageCmd, logsCronCmd,
	} {
		c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
		c.Flags().BoolP("follow", "f", false, "Poll for new log lines every 2 seconds")
	}

	// --tail flag (not on build since it pages through the full deployment log)
	for _, c := range []*cobra.Command{
		logsRuntimeCmd, logsLambdaCmd, logsWorkersCmd, logsStorageCmd, logsCronCmd,
	} {
		c.Flags().Int("tail", 100, "Number of log entries to fetch (max 1000)")
	}

	logsCmd.AddCommand(logsRuntimeCmd, logsBuildCmd, logsLambdaCmd, logsWorkersCmd, logsStorageCmd, logsCronCmd)
	rootCmd.AddCommand(logsCmd)
}
