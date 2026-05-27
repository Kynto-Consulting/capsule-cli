package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type projectListResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Slug    string `json:"slug"`
		Status  string `json:"status"`
		Runtime string `json:"runtime"`
	} `json:"data"`
}

var projectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "Manage projects",
}

var projectsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List projects for an org",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, _ := cmd.Flags().GetString("org")
		if orgID == "" {
			orgID = cfg.OrgID
		}
		if orgID == "" {
			return fmt.Errorf("--org is required (or set org_id in ~/.capsule/config.yaml)")
		}
		var resp projectListResponse
		if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/projects", orgID), &resp); err != nil {
			return err
		}
		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp.Data)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSLUG\tSTATUS\tRUNTIME")
		for _, p := range resp.Data {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Slug, p.Status, p.Runtime)
		}
		return w.Flush()
	},
}

var projectsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, _ := cmd.Flags().GetString("org")
		if orgID == "" {
			orgID = cfg.OrgID
		}
		if orgID == "" {
			return fmt.Errorf("--org is required")
		}
		name, _ := cmd.Flags().GetString("name")
		slug, _ := cmd.Flags().GetString("slug")
		runtime, _ := cmd.Flags().GetString("runtime")
		repoURL, _ := cmd.Flags().GetString("repo")
		if name == "" || slug == "" {
			return fmt.Errorf("--name and --slug are required")
		}
		body := map[string]string{"name": name, "slug": slug, "runtime": runtime, "repo_url": repoURL}
		var resp map[string]any
		if err := apiClient.Post(fmt.Sprintf("/api/v1/orgs/%s/projects", orgID), body, &resp); err != nil {
			return err
		}
		fmt.Printf("Created project %s (%s)\n", resp["name"], resp["id"])
		return nil
	},
}

func init() {
	projectsListCmd.Flags().String("org", "", "Org ID")
	projectsCreateCmd.Flags().String("org", "", "Org ID")
	projectsCreateCmd.Flags().String("name", "", "Project name")
	projectsCreateCmd.Flags().String("slug", "", "Project slug")
	projectsCreateCmd.Flags().String("runtime", "", "Runtime (go, node, python, rust)")
	projectsCreateCmd.Flags().String("repo", "", "Repository URL")
	projectsCmd.AddCommand(projectsListCmd, projectsCreateCmd)
	rootCmd.AddCommand(projectsCmd)
}
