package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var scaleCmd = &cobra.Command{
	Use:   "scale [project-slug]",
	Short: "Configure horizontal scaling and auto-scaling thresholds",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, _ := cmd.Flags().GetString("org")
		if orgID == "" {
			orgID = cfg.OrgID
		}
		if orgID == "" {
			return fmt.Errorf("--org is required")
		}

		projectSlug := args[0]
		replicas, _ := cmd.Flags().GetInt("replicas")
		min, _ := cmd.Flags().GetInt("min")
		max, _ := cmd.Flags().GetInt("max")
		cpu, _ := cmd.Flags().GetInt("cpu-threshold")

		// Retrieve project record by slug
		type project struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		var listResp struct {
			Data []project `json:"data"`
		}
		path := fmt.Sprintf("/api/v1/orgs/%s/projects", orgID)
		if err := apiClient.Get(path, &listResp); err != nil {
			return err
		}

		var projID string
		for _, p := range listResp.Data {
			if p.Slug == projectSlug {
				projID = p.ID
				break
			}
		}

		if projID == "" {
			return fmt.Errorf("project not found: %s", projectSlug)
		}

		body := map[string]any{}
		if cmd.Flags().Changed("replicas") {
			body["replicas"] = replicas
		}

		// Inject custom labels/metadata for scaling bounds & thresholds
		labels := map[string]any{}
		if cmd.Flags().Changed("min") {
			labels["min_replicas"] = min
		}
		if cmd.Flags().Changed("max") {
			labels["max_replicas"] = max
		}
		if cmd.Flags().Changed("cpu-threshold") {
			labels["cpu_threshold"] = cpu
		}
		if len(labels) > 0 {
			body["labels"] = labels
		}

		if len(body) == 0 {
			return fmt.Errorf("no scaling parameters specified; specify --replicas, or --min, --max, --cpu-threshold")
		}

		var resp map[string]any
		patchPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s", orgID, projID)
		if err := apiClient.Patch(patchPath, body, &resp); err != nil {
			return err
		}

		fmt.Printf("Successfully updated scaling configurations for %s!\n", projectSlug)
		if cmd.Flags().Changed("replicas") {
			fmt.Printf("Replicas: %d\n", replicas)
		}
		if cmd.Flags().Changed("min") || cmd.Flags().Changed("max") {
			fmt.Printf("Auto-scaling: bounds [%d - %d], CPU trigger: %d%%\n", min, max, cpu)
		}
		return nil
	},
}

func init() {
	scaleCmd.Flags().String("org", "", "Org ID")
	scaleCmd.Flags().Int("replicas", 1, "Static replicas count")
	scaleCmd.Flags().Int("min", 1, "Minimum replicas bounds")
	scaleCmd.Flags().Int("max", 5, "Maximum replicas bounds")
	scaleCmd.Flags().Int("cpu-threshold", 75, "Auto-scaling CPU trigger percentage")

	rootCmd.AddCommand(scaleCmd)
}
