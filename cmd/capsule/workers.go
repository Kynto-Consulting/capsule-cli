package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var workersCmd = &cobra.Command{
	Use:   "workers",
	Short: "Manage background workers",
}

var workersCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a background worker",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		command, _ := cmd.Flags().GetString("command")
		restart, _ := cmd.Flags().GetString("restart")

		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if command == "" {
			return fmt.Errorf("--command is required")
		}

		body := map[string]any{
			"name":           name,
			"command":        command,
			"restart_policy": restart,
			"replicas":       1,
		}
		var resp map[string]any
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/workers", orgID, projectID)
		if err := apiClient.Post(path, body, &resp); err != nil {
			return err
		}
		fmt.Printf("Created worker %s (id: %s)\n", resp["name"], resp["id"])
		return nil
	},
}

var workersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List background workers",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		type workerItem struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Status        string `json:"status"`
			RestartPolicy string `json:"restart_policy"`
		}
		var resp struct {
			Data []workerItem `json:"data"`
		}
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/workers", orgID, projectID)
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		if len(resp.Data) == 0 {
			fmt.Println("No workers found.")
			return nil
		}

		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp.Data)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tRESTART")
		for _, wk := range resp.Data {
			shortID := wk.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", shortID, wk.Name, wk.Status, wk.RestartPolicy)
		}
		return w.Flush()
	},
}

var workersGetCmd = &cobra.Command{
	Use:   "get [worker-id]",
	Short: "Get a background worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/workers/%s", orgID, projectID, args[0])
		var resp map[string]any
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp)
		}
		fmt.Printf("ID:      %s\n", resp["id"])
		fmt.Printf("Name:    %s\n", resp["name"])
		fmt.Printf("Status:  %s\n", resp["status"])
		fmt.Printf("Command: %s\n", resp["command"])
		fmt.Printf("Restart: %s\n", resp["restart_policy"])
		return nil
	},
}

var workersDeleteCmd = &cobra.Command{
	Use:   "delete [worker-id]",
	Short: "Delete a background worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/workers/%s", orgID, projectID, args[0])
		if err := apiClient.Delete(path); err != nil {
			return err
		}
		fmt.Println("Worker deleted")
		return nil
	},
}

var workersStartCmd = &cobra.Command{
	Use:   "start [worker-id]",
	Short: "Start a worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/workers/%s/start", orgID, projectID, args[0])
		var resp map[string]any
		if err := apiClient.Post(path, nil, &resp); err != nil {
			return err
		}
		fmt.Println("Worker started")
		return nil
	},
}

var workersStopCmd = &cobra.Command{
	Use:   "stop [worker-id]",
	Short: "Stop a worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/workers/%s/stop", orgID, projectID, args[0])
		var resp map[string]any
		if err := apiClient.Post(path, nil, &resp); err != nil {
			return err
		}
		fmt.Println("Worker stopped")
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{
		workersCreateCmd, workersListCmd, workersGetCmd, workersDeleteCmd,
		workersStartCmd, workersStopCmd,
	} {
		c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	}

	workersCreateCmd.Flags().String("name", "", "Worker name")
	workersCreateCmd.Flags().String("command", "", "Command to run")
	workersCreateCmd.Flags().String("restart", "unless-stopped", "Restart policy (always/unless-stopped/on-failure/no)")

	workersListCmd.Flags().String("output", "", "Output format (json)")
	workersGetCmd.Flags().String("output", "", "Output format (json)")

	workersCmd.AddCommand(
		workersCreateCmd,
		workersListCmd,
		workersGetCmd,
		workersDeleteCmd,
		workersStartCmd,
		workersStopCmd,
	)
	rootCmd.AddCommand(workersCmd)
}
