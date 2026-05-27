package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type projectListItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Status     string `json:"status"`
	Runtime    string `json:"runtime"`
	DeployType string `json:"deploy_type"`
	CreatedAt  string `json:"created_at"`
}

type projectListResponse struct {
	Data []projectListItem `json:"data"`
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
		fmt.Fprintln(w, "NAME\tSLUG\tSTATUS\tDEPLOY_TYPE\tCREATED")
		for _, p := range resp.Data {
			created := p.CreatedAt
			if len(created) > 10 {
				created = created[:10]
			}
			deployType := p.DeployType
			if deployType == "" {
				deployType = p.Runtime
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Slug, p.Status, deployType, created)
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

// resolveProjectID looks up a project by slug within an org and returns its UUID.
// It uses the list endpoint since the delete endpoint requires a UUID.
func resolveProjectID(orgID, slug string) (string, error) {
	var resp projectListResponse
	if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/projects", orgID), &resp); err != nil {
		return "", err
	}
	for _, p := range resp.Data {
		if p.Slug == slug || p.ID == slug {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("project %q not found in org %q", slug, orgID)
}

var projectsDeleteCmd = &cobra.Command{
	Use:   "delete [slug]",
	Short: "Permanently delete a project and all its resources",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, _ := cmd.Flags().GetString("org")
		if orgID == "" {
			orgID = cfg.OrgID
		}
		if orgID == "" {
			return fmt.Errorf("--org is required (or set org_id in ~/.capsule/config.yaml)")
		}

		// Determine slug: from arg or from linked project config
		var slug string
		if len(args) > 0 {
			slug = args[0]
		} else {
			_, projectID, err := resolveOrgProject(cmd)
			if err != nil {
				return fmt.Errorf("slug argument or linked project required: %w", err)
			}
			slug = projectID
		}

		// Resolve slug → UUID (also validates the project exists)
		projectID, err := resolveProjectID(orgID, slug)
		if err != nil {
			return err
		}
		// If the caller passed an ID directly, keep slug human-readable for messages
		// resolveProjectID accepts either; normalise slug to the user-supplied value.

		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			fmt.Printf("This will permanently delete project '%s' and all its resources.\n", slug)
			fmt.Printf("Type the project slug to confirm: ")
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(input)
			if input != slug {
				fmt.Println("Cancelled.")
				return nil
			}
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s", orgID, projectID)
		if err := apiClient.Delete(path); err != nil {
			return err
		}
		fmt.Printf("Project '%s' deleted.\n", slug)
		return nil
	},
}

var projectsArchiveCmd = &cobra.Command{
	Use:   "archive [slug]",
	Short: "Archive a project (not yet supported — use 'delete' instead)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Archive not yet supported — use 'capsule projects delete' to remove a project.")
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
	projectsDeleteCmd.Flags().String("org", "", "Org ID")
	projectsDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	projectsArchiveCmd.Flags().String("org", "", "Org ID")
	projectsCmd.AddCommand(projectsListCmd, projectsCreateCmd, projectsDeleteCmd, projectsArchiveCmd)
	rootCmd.AddCommand(projectsCmd)
}
