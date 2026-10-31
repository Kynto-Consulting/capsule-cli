package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var tokensCmd = &cobra.Command{
	Use:   "tokens",
	Short: "Manage API tokens (cap_...)",
}

type createdToken struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Token  string `json:"token"`
	Prefix string `json:"prefix"`
}

// createAPIToken creates a token through POST /user/tokens.
func createAPIToken(name, scope, projectID string) (*createdToken, error) {
	body := map[string]interface{}{"name": name}
	if scope != "" {
		body["scopes"] = scope
	}
	if projectID != "" {
		body["project_id"] = projectID
	}
	var t createdToken
	if err := apiClient.Post("/api/v1/user/tokens", body, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

var tokensCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an API token",
	Long: `Create an API token. The token value is shown once.
For CI use --scope deploy --project <project-id>: the token can only deploy
that project. Store it as the CAPSULE_TOKEN secret.`,
	Example: `  capsule tokens create --name github-ci --scope deploy --project <project-id>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		scope, _ := cmd.Flags().GetString("scope")
		project, _ := cmd.Flags().GetString("project")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if scope == "deploy" && project == "" {
			return fmt.Errorf("--scope deploy requires --project (tokens are limited to one project)")
		}
		if project != "" && len(project) < 36 {
			if org, err := resolveOrg(cmd); err == nil {
				if id, err := resolveIDPrefix("/api/v1/orgs/"+org+"/projects", "project", project); err == nil {
					project = id
				}
			}
		}
		t, err := createAPIToken(name, scope, project)
		if err != nil {
			return err
		}
		fmt.Printf("Token %q created (id %s). Copy it now, it will not be shown again:\n\n%s\n", t.Name, t.ID, t.Token)
		return nil
	},
}

var tokensListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your API tokens",
	RunE: func(cmd *cobra.Command, args []string) error {
		var toks []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
			Scopes string `json:"scopes"`
		}
		if err := apiClient.Get("/api/v1/user/tokens", &toks); err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPREFIX\tSCOPES")
		for _, t := range toks {
			id := t.ID
			if len(id) > 8 {
				id = id[:8]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, t.Name, t.Prefix, t.Scopes)
		}
		return w.Flush()
	},
}

var tokensRevokeCmd = &cobra.Command{
	Use:   "revoke TOKEN_ID",
	Short: "Revoke an API token (full ID)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.Delete("/api/v1/user/tokens/" + args[0]); err != nil {
			return err
		}
		fmt.Println("Revoked", args[0])
		return nil
	},
}

func init() {
	tokensCreateCmd.Flags().String("name", "", "Token name")
	tokensCreateCmd.Flags().String("scope", "", "Token scope, e.g. deploy (default: full access)")
	tokensCreateCmd.Flags().String("project", "", "Restrict the token to this project ID")
	tokensCreateCmd.Flags().String("org", "", "Org ID (only used to expand a short --project ID)")
	tokensCmd.AddCommand(tokensCreateCmd, tokensListCmd, tokensRevokeCmd)
	rootCmd.AddCommand(tokensCmd)
}
