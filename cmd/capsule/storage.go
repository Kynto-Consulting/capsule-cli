package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var storageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Manage S3 storage buckets",
}

var storageCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an S3 bucket",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, _ := cmd.Flags().GetString("org")
		if orgID == "" {
			orgID = cfg.OrgID
		}
		projectID, _ := cmd.Flags().GetString("project")
		name, _ := cmd.Flags().GetString("name")

		if orgID == "" || projectID == "" || name == "" {
			return fmt.Errorf("--org, --project, and --name are required")
		}

		// Show cost preview before confirming
		fmt.Println("┌─ Cost Estimate ──────────────────────────────┐")
		fmt.Println("│ Amazon S3 Standard Bucket                    │")
		fmt.Println("│                                              │")
		fmt.Println("│  Storage:      $0.23/month (10 GB avg)       │")
		fmt.Println("│  Requests:     $0.04/month (100K GETs)       │")
		fmt.Println("│  CloudFront:   $0.85/month (10 GB out)       │")
		fmt.Println("│  ─────────────────────────────               │")
		fmt.Println("│  Estimated:    ~$1.12/month                  │")
		fmt.Println("│  Annual:       ~$13.44/year                  │")
		fmt.Println("└──────────────────────────────────────────────┘")
		fmt.Print("Proceed with provisioning S3 bucket? (y/N): ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) != "y" && strings.ToLower(confirm) != "yes" {
			fmt.Println("Provisioning cancelled.")
			return nil
		}

		body := map[string]string{"name": name}
		var resp map[string]any
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/storage", orgID, projectID)
		if err := apiClient.Post(path, body, &resp); err != nil {
			return err
		}

		fmt.Printf("Created S3 Bucket %s (Status: %s)\n", resp["db_name"], resp["status"])
		return nil
	},
}

var storageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List S3 buckets for a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, _ := cmd.Flags().GetString("org")
		if orgID == "" {
			orgID = cfg.OrgID
		}
		projectID, _ := cmd.Flags().GetString("project")

		if orgID == "" || projectID == "" {
			return fmt.Errorf("--org and --project are required")
		}

		type bucket struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Engine string `json:"engine"`
			DBName string `json:"db_name"`
			Status string `json:"status"`
			Host   string `json:"host"`
		}
		var resp struct {
			Data []bucket `json:"data"`
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/storage", orgID, projectID)
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp.Data)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tBUCKET NAME\tSTATUS\tENDPOINT")
		for _, b := range resp.Data {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", b.ID, b.Name, b.DBName, b.Status, b.Host)
		}
		return w.Flush()
	},
}

var storagePresignCmd = &cobra.Command{
	Use:   "presign [s3://bucket-name/key-path]",
	Short: "Generate a presigned GET URL for an object",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, _ := cmd.Flags().GetString("org")
		if orgID == "" {
			orgID = cfg.OrgID
		}
		projectID, _ := cmd.Flags().GetString("project")
		expires, _ := cmd.Flags().GetInt("expires")

		if orgID == "" || projectID == "" {
			return fmt.Errorf("--org and --project are required")
		}

		uri := args[0]
		if !strings.HasPrefix(uri, "s3://") {
			return fmt.Errorf("invalid S3 URI; must start with s3://")
		}

		parts := strings.SplitN(strings.TrimPrefix(uri, "s3://"), "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			return fmt.Errorf("invalid S3 URI format; must be s3://bucket-name/object-key")
		}
		bucketName := parts[0]
		objectKey := parts[1]

		// First list storage buckets to resolve bucket ID
		type bucket struct {
			ID     string `json:"id"`
			DBName string `json:"db_name"`
		}
		var listResp struct {
			Data []bucket `json:"data"`
		}
		listPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/storage", orgID, projectID)
		if err := apiClient.Get(listPath, &listResp); err != nil {
			return err
		}

		var bucketID string
		for _, b := range listResp.Data {
			if b.DBName == bucketName {
				bucketID = b.ID
				break
			}
		}

		if bucketID == "" {
			return fmt.Errorf("could not find S3 bucket record for name: %s", bucketName)
		}

		body := map[string]any{
			"key":     objectKey,
			"expires": expires,
		}
		var resp map[string]string
		presignPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/storage/%s/presign", orgID, projectID, bucketID)
		if err := apiClient.Post(presignPath, body, &resp); err != nil {
			return err
		}

		fmt.Println("Presigned GET URL generated:")
		fmt.Println(resp["url"])
		return nil
	},
}

func init() {
	storageCreateCmd.Flags().String("org", "", "Org ID")
	storageCreateCmd.Flags().String("project", "", "Project ID")
	storageCreateCmd.Flags().String("name", "", "Bucket reference name")

	storageListCmd.Flags().String("org", "", "Org ID")
	storageListCmd.Flags().String("project", "", "Project ID")

	storagePresignCmd.Flags().String("org", "", "Org ID")
	storagePresignCmd.Flags().String("project", "", "Project ID")
	storagePresignCmd.Flags().Int("expires", 3600, "Expiration time in seconds")

	storageCmd.AddCommand(storageCreateCmd, storageListCmd, storagePresignCmd)
	rootCmd.AddCommand(storageCmd)
}
