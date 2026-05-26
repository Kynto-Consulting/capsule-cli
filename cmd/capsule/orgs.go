package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type orgListResponse struct {
	Data []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Plan string `json:"plan"`
	} `json:"data"`
}

var orgsCmd = &cobra.Command{
	Use:   "orgs",
	Short: "Manage organizations",
}

var orgsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List organizations",
	RunE: func(cmd *cobra.Command, args []string) error {
		var resp orgListResponse
		if err := apiClient.Get("/api/v1/orgs", &resp); err != nil {
			return err
		}
		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp.Data)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSLUG\tPLAN")
		for _, o := range resp.Data {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.ID, o.Name, o.Slug, o.Plan)
		}
		return w.Flush()
	},
}

var orgsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an organization",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		slug, _ := cmd.Flags().GetString("slug")
		if name == "" || slug == "" {
			return fmt.Errorf("--name and --slug are required")
		}
		var resp map[string]any
		if err := apiClient.Post("/api/v1/orgs", map[string]string{"name": name, "slug": slug}, &resp); err != nil {
			return err
		}
		fmt.Printf("Created org %s (%s)\n", resp["name"], resp["id"])
		return nil
	},
}

func init() {
	orgsCreateCmd.Flags().String("name", "", "Organization name")
	orgsCreateCmd.Flags().String("slug", "", "Organization slug")
	orgsCmd.AddCommand(orgsListCmd, orgsCreateCmd)
	rootCmd.AddCommand(orgsCmd)
}
