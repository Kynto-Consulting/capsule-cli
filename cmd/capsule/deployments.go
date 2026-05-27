package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// deploymentsAPIItem maps to a single deployment returned by the API.
type deploymentsAPIItem struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	Version     string  `json:"version"`
	GitSHA      string  `json:"git_sha"`
	Trigger     string  `json:"trigger"`
	CreatedAt   string  `json:"created_at"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	HostPort    *int    `json:"host_port"`
	SourceKey   *string `json:"source_key"`
}

type deploymentsListResponse struct {
	Data []deploymentsAPIItem `json:"data"`
	Meta struct {
		Page    int `json:"page"`
		PerPage int `json:"per_page"`
		Total   int `json:"total"`
	} `json:"meta"`
}

// formatAge converts an RFC3339 timestamp string to a human-readable age.
func formatAge(ts string) string {
	if ts == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// Try without nanoseconds
		t, err = time.Parse("2006-01-02T15:04:05Z", ts)
		if err != nil {
			return "—"
		}
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// deploymentURL is kept for backwards compatibility but no longer emits
// an instance-specific URL (the host port is internal and not user-facing).
func deploymentURL(hostPort *int) string {
	return "—"
}

// resolveOrgProject returns orgID and projectID from flags or .capsule.json.
func resolveOrgProject(cmd *cobra.Command) (string, string, error) {
	orgID, _ := cmd.Flags().GetString("org")
	projectID, _ := cmd.Flags().GetString("project")

	if orgID == "" || projectID == "" {
		cwd, _ := os.Getwd()
		pc, _, err := config.FindProjectConfig(cwd)
		if err == nil {
			if orgID == "" {
				orgID = pc.OrgID
			}
			if projectID == "" {
				projectID = pc.ProjectID
			}
		}
	}

	if orgID == "" {
		return "", "", fmt.Errorf("--org is required (or link directory with: capsule link)")
	}
	if projectID == "" {
		return "", "", fmt.Errorf("--project is required (or link directory with: capsule link)")
	}
	return orgID, projectID, nil
}

// ── deployments command group ────────────────────────────────────────────────

var deploymentsCmd = &cobra.Command{
	Use:   "deployments",
	Short: "Manage deployments",
}

// ── deployments list ─────────────────────────────────────────────────────────

var deploymentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List deployments for a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		var resp deploymentsListResponse
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments", orgID, projectID)
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tVERSION\tGIT_SHA\tTRIGGER\tAGE\tURL")
		for _, d := range resp.Data {
			sha := d.GitSHA
			if len(sha) > 7 {
				sha = sha[:7]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				d.ID, d.Status, d.Version, sha, d.Trigger,
				formatAge(d.CreatedAt), deploymentURL(d.HostPort),
			)
		}
		return w.Flush()
	},
}

// ── deployments get ──────────────────────────────────────────────────────────

var deploymentsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get details of a deployment",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}

		var d deploymentsAPIItem
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/%s", orgID, projectID, id)
		if err := apiClient.Get(path, &d); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "ID\t%s\n", d.ID)
		fmt.Fprintf(w, "Status\t%s\n", d.Status)
		fmt.Fprintf(w, "Version\t%s\n", d.Version)
		fmt.Fprintf(w, "Git SHA\t%s\n", d.GitSHA)
		fmt.Fprintf(w, "Trigger\t%s\n", d.Trigger)
		fmt.Fprintf(w, "Created\t%s\n", formatAge(d.CreatedAt))
		if d.StartedAt != nil {
			fmt.Fprintf(w, "Started\t%s\n", formatAge(*d.StartedAt))
		}
		if d.CompletedAt != nil {
			fmt.Fprintf(w, "Completed\t%s\n", formatAge(*d.CompletedAt))
		}
		if d.HostPort != nil && *d.HostPort > 0 {
			fmt.Fprintf(w, "URL\t%s\n", deploymentURL(d.HostPort))
		}
		return w.Flush()
	},
}

// ── deployments logs ─────────────────────────────────────────────────────────

type buildLogItem struct {
	ID        string `json:"id"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

var deploymentsLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Stream logs for a deployment",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		follow, _ := cmd.Flags().GetBool("follow")

		logsPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/%s/logs", orgID, projectID, id)
		depPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/%s", orgID, projectID, id)

		terminalStatuses := map[string]bool{
			"success":   true,
			"failed":    true,
			"cancelled": true,
			"error":     true,
			"timeout":   true,
		}

		printLogs := func(logs []buildLogItem, seen map[string]bool) {
			for _, l := range logs {
				if seen[l.ID] {
					continue
				}
				seen[l.ID] = true
				prefix := "·"
				if l.Level == "error" {
					prefix = "✗"
				}
				fmt.Printf("[%s] %s %s\n", l.Level, prefix, l.Message)
			}
		}

		var logs []buildLogItem
		if err := apiClient.Get(logsPath, &logs); err != nil {
			return err
		}

		seen := make(map[string]bool)
		printLogs(logs, seen)

		if !follow {
			return nil
		}

		for {
			time.Sleep(2 * time.Second)

			var newLogs []buildLogItem
			if err := apiClient.Get(logsPath, &newLogs); err == nil {
				printLogs(newLogs, seen)
			}

			var d deploymentsAPIItem
			if err := apiClient.Get(depPath, &d); err == nil && terminalStatuses[d.Status] {
				break
			}
		}
		return nil
	},
}

// ── deployments cancel ───────────────────────────────────────────────────────

var deploymentsCancelCmd = &cobra.Command{
	Use:   "cancel",
	Short: "Cancel a deployment",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/%s/cancel", orgID, projectID, id)
		var resp map[string]interface{}
		if err := apiClient.Post(path, nil, &resp); err != nil {
			return err
		}
		fmt.Printf("Deployment %s cancelled\n", id)
		return nil
	},
}

// ── deployments rollback ─────────────────────────────────────────────────────

var deploymentsRollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Roll back to the previous successful deployment",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		// List deployments
		var resp deploymentsListResponse
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments", orgID, projectID)
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		if len(resp.Data) == 0 {
			return fmt.Errorf("no deployments found")
		}

		// Find the most recent 'success' deployment that is not the latest
		var target *deploymentsAPIItem
		skippedLatest := false
		for i := range resp.Data {
			d := &resp.Data[i]
			if !skippedLatest {
				skippedLatest = true
				continue // skip the most recent deployment
			}
			if d.Status == "success" {
				target = d
				break
			}
		}

		if target == nil {
			return fmt.Errorf("no previous successful deployment found to roll back to")
		}

		fmt.Printf("Rolling back to deployment %s", target.ID)
		if target.GitSHA != "" {
			fmt.Printf(" (git sha: %s)", target.GitSHA)
		}
		fmt.Println()

		// Trigger a new deployment with the same source_key
		body := map[string]string{
			"version": "rollback",
			"trigger": "rollback",
		}
		if target.SourceKey != nil && *target.SourceKey != "" {
			body["source_key"] = *target.SourceKey
		}
		if target.GitSHA != "" {
			body["git_sha"] = target.GitSHA
		}

		var newDep deploymentsAPIItem
		if err := apiClient.Post(path, body, &newDep); err != nil {
			return err
		}

		fmt.Printf("⏳  queued  0s\n")
		return pollDeployment(orgID, projectID, newDep.ID)
	},
}

// ── init ─────────────────────────────────────────────────────────────────────

func init() {
	// list flags
	deploymentsListCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	deploymentsListCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")

	// get flags
	deploymentsGetCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	deploymentsGetCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	deploymentsGetCmd.Flags().String("id", "", "Deployment ID (required)")

	// logs flags
	deploymentsLogsCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	deploymentsLogsCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	deploymentsLogsCmd.Flags().String("id", "", "Deployment ID (required)")
	deploymentsLogsCmd.Flags().Bool("follow", false, "Poll for new log lines until deployment is terminal")

	// cancel flags
	deploymentsCancelCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	deploymentsCancelCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	deploymentsCancelCmd.Flags().String("id", "", "Deployment ID (required)")

	// rollback flags
	deploymentsRollbackCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	deploymentsRollbackCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")

	deploymentsCmd.AddCommand(
		deploymentsListCmd,
		deploymentsGetCmd,
		deploymentsLogsCmd,
		deploymentsCancelCmd,
		deploymentsRollbackCmd,
	)
	rootCmd.AddCommand(deploymentsCmd)
}
