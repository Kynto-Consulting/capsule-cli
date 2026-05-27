package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var cronsCmd = &cobra.Command{
	Use:   "crons",
	Short: "Manage scheduled cron jobs",
}

var cronsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a cron job",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		schedule, _ := cmd.Flags().GetString("schedule")
		command, _ := cmd.Flags().GetString("command")
		timezone, _ := cmd.Flags().GetString("timezone")
		if name == "" || schedule == "" || command == "" {
			return fmt.Errorf("--name, --schedule, and --command are required")
		}
		body := map[string]any{"name": name, "schedule": schedule, "command": command, "timezone": timezone}
		var resp map[string]any
		if err := apiClient.Post(fmt.Sprintf("/api/v1/orgs/%s/projects/%s/crons", orgID, projectID), body, &resp); err != nil {
			return err
		}
		fmt.Printf("Created cron job %s (id: %s)\n", resp["name"], resp["id"])
		fmt.Printf("Schedule: %s (%s)\n", resp["schedule"], resp["timezone"])
		return nil
	},
}

var cronsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cron jobs",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		type cronItem struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Schedule  string  `json:"schedule"`
			Status    string  `json:"status"`
			NextRunAt *string `json:"next_run_at"`
		}
		var resp struct{ Data []cronItem `json:"data"` }
		if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/projects/%s/crons", orgID, projectID), &resp); err != nil {
			return err
		}
		if len(resp.Data) == 0 {
			fmt.Println("No cron jobs found.")
			return nil
		}
		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp.Data)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tSCHEDULE\tSTATUS\tNEXT RUN")
		for _, c := range resp.Data {
			id := c.ID
			if len(id) > 8 { id = id[:8] }
			next := "-"
			if c.NextRunAt != nil { next = *c.NextRunAt }
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", id, c.Name, c.Schedule, c.Status, next)
		}
		return tw.Flush()
	},
}

var cronsGetCmd = &cobra.Command{
	Use:   "get [cron-id]",
	Short: "Get a cron job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		var resp map[string]any
		if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/projects/%s/crons/%s", orgID, projectID, args[0]), &resp); err != nil {
			return err
		}
		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp)
		}
		fmt.Printf("ID:          %s\n", resp["id"])
		fmt.Printf("Name:        %s\n", resp["name"])
		fmt.Printf("Schedule:    %s\n", resp["schedule"])
		fmt.Printf("Timezone:    %s\n", resp["timezone"])
		fmt.Printf("Command:     %s\n", resp["command"])
		fmt.Printf("Status:      %s\n", resp["status"])
		fmt.Printf("Last run:    %v\n", resp["last_run_at"])
		fmt.Printf("Last status: %v\n", resp["last_run_status"])
		fmt.Printf("Next run:    %v\n", resp["next_run_at"])
		return nil
	},
}

var cronsDeleteCmd = &cobra.Command{
	Use:   "delete [cron-id]",
	Short: "Delete a cron job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		if err := apiClient.Delete(fmt.Sprintf("/api/v1/orgs/%s/projects/%s/crons/%s", orgID, projectID, args[0])); err != nil {
			return err
		}
		fmt.Println("Cron job deleted")
		return nil
	},
}

var cronsTriggerCmd = &cobra.Command{
	Use:   "trigger [cron-id]",
	Short: "Trigger a cron job immediately",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		var resp map[string]any
		if err := apiClient.Post(fmt.Sprintf("/api/v1/orgs/%s/projects/%s/crons/%s/trigger", orgID, projectID, args[0]), nil, &resp); err != nil {
			return err
		}
		fmt.Println("Cron job triggered")
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{cronsCreateCmd, cronsListCmd, cronsGetCmd, cronsDeleteCmd, cronsTriggerCmd} {
		c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	}
	cronsCreateCmd.Flags().String("name", "", "Cron job name")
	cronsCreateCmd.Flags().String("schedule", "", "Cron expression (e.g. '0 2 * * *')")
	cronsCreateCmd.Flags().String("command", "", "Command to execute")
	cronsCreateCmd.Flags().String("timezone", "UTC", "Timezone (e.g. America/New_York)")
	cronsListCmd.Flags().String("output", "", "Output format (json)")
	cronsGetCmd.Flags().String("output", "", "Output format (json)")
	cronsCmd.AddCommand(cronsCreateCmd, cronsListCmd, cronsGetCmd, cronsDeleteCmd, cronsTriggerCmd)
	rootCmd.AddCommand(cronsCmd)
}
