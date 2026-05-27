package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// ── API types ────────────────────────────────────────────────────────────────

type domainItem struct {
	ID                string `json:"id"`
	DomainName        string `json:"domain_name"`
	Status            string `json:"status"`
	RecordType        string `json:"record_type"`
	RecordValue       string `json:"record_value"`
	VerificationToken string `json:"verification_token"`
	SSLEnabled        bool   `json:"ssl_enabled"`
	DNSProvider       string `json:"dns_provider"`
	VerifiedAt        string `json:"verified_at"`
}

// ── commands ──────────────────────────────────────────────────────────────────

var domainsCmd = &cobra.Command{
	Use:   "domains",
	Short: "Manage custom domains for a project",
}

var domainsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List domains attached to a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		var resp struct {
			Data []domainItem `json:"data"`
		}
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/domains", orgID, projectID)
		if err := apiClient.Get(path, &resp); err != nil {
			return fmt.Errorf("fetching domains: %w", err)
		}

		if len(resp.Data) == 0 {
			fmt.Println("No domains found. Add one with: capsule domains add <domain>")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tDOMAIN\tSTATUS\tSSL\tVERIFIED_AT")
		for _, d := range resp.Data {
			ssl := "no"
			if d.SSLEnabled {
				ssl = "yes"
			}
			verifiedAt := d.VerifiedAt
			if verifiedAt == "" {
				verifiedAt = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.DomainName, d.Status, ssl, verifiedAt)
		}
		return w.Flush()
	},
}

var domainsAddCmd = &cobra.Command{
	Use:   "add <domain>",
	Short: "Add a custom domain to a project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		domain := args[0]

		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		dnsType, _ := cmd.Flags().GetString("type")
		redirectTo, _ := cmd.Flags().GetString("redirect-to")

		dnsProvider := "custom"
		if dnsType == "redirect" && redirectTo != "" {
			dnsProvider = "redirect:" + redirectTo
		}

		body := map[string]interface{}{
			"domain_name": domain,
			"dns_provider": dnsProvider,
			"record_type": "CNAME",
			"ssl_enabled": true,
		}

		var result domainItem
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/domains", orgID, projectID)
		if err := apiClient.Post(path, body, &result); err != nil {
			return fmt.Errorf("adding domain: %w", err)
		}

		recordValue := result.RecordValue
		if recordValue == "" {
			recordValue = "capsule-alb-1179394433.us-east-1.elb.amazonaws.com"
		}
		recordType := result.RecordType
		if recordType == "" {
			recordType = "CNAME"
		}

		fmt.Println("Domain added. To verify, add this DNS record:")
		fmt.Println()
		fmt.Printf("  Type:  %s\n", recordType)
		fmt.Printf("  Name:  %s\n", domain)
		fmt.Printf("  Value: %s\n", recordValue)
		fmt.Println()
		fmt.Printf("Then run: capsule domains verify %s\n", result.ID)
		return nil
	},
}

var domainsVerifyCmd = &cobra.Command{
	Use:   "verify <id>",
	Short: "Verify DNS for a custom domain",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]

		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		var result struct {
			Verified bool   `json:"verified"`
			Message  string `json:"message"`
		}
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/domains/%s/verify", orgID, projectID, id)
		if err := apiClient.Post(path, map[string]string{}, &result); err != nil {
			return fmt.Errorf("verifying domain: %w", err)
		}

		if result.Verified {
			fmt.Println("✓ Domain verified")
		} else {
			msg := result.Message
			if msg == "" {
				msg = "check DNS record"
			}
			fmt.Printf("✗ Verification failed — %s\n", msg)
		}
		return nil
	},
}

var domainsRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a custom domain from a project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]

		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/domains/%s", orgID, projectID, id)
		if err := apiClient.Delete(path); err != nil {
			return fmt.Errorf("removing domain: %w", err)
		}

		fmt.Println("Domain removed")
		return nil
	},
}

func init() {
	// Shared flags for all subcommands
	for _, sub := range []*cobra.Command{domainsListCmd, domainsAddCmd, domainsVerifyCmd, domainsRemoveCmd} {
		sub.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		sub.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	}

	// Extra flags for add
	domainsAddCmd.Flags().String("type", "direct", "Domain type: direct | redirect")
	domainsAddCmd.Flags().String("redirect-to", "", "Redirect target URL (for --type=redirect)")

	domainsCmd.AddCommand(domainsListCmd)
	domainsCmd.AddCommand(domainsAddCmd)
	domainsCmd.AddCommand(domainsVerifyCmd)
	domainsCmd.AddCommand(domainsRemoveCmd)

	rootCmd.AddCommand(domainsCmd)
}
