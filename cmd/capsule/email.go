package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// ── email command group ──────────────────────────────────────────────────────

var emailCmd = &cobra.Command{
	Use:   "email",
	Short: "Manage AWS SES email domain integrations",
}

// ── email setup ──────────────────────────────────────────────────────────────

var emailSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Setup an email sending domain with SES",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		domainName, _ := cmd.Flags().GetString("domain")
		if domainName == "" {
			return fmt.Errorf("--domain is required")
		}

		body := map[string]string{"domain": domainName}
		var resp map[string]any
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/setup", orgID, projectID)
		if err := apiClient.Post(path, body, &resp); err != nil {
			return err
		}

		fmt.Printf("Domain registered. Add DNS records to verify.\n")
		fmt.Printf("\nRun 'capsule email dns --org %s --project %s' to view required DNS records.\n", orgID, projectID)
		return nil
	},
}

// ── email test ───────────────────────────────────────────────────────────────

var emailTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Send a test email from the verified domain",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		toEmail, _ := cmd.Flags().GetString("to")
		if toEmail == "" {
			return fmt.Errorf("--to is required")
		}

		body := map[string]string{"to": toEmail}
		var resp map[string]string
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/test", orgID, projectID)
		if err := apiClient.Post(path, body, &resp); err != nil {
			return err
		}

		fmt.Println(resp["message"])
		return nil
	},
}

// ── email stats ──────────────────────────────────────────────────────────────

var emailStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "View SES sending and bounce analytics",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		type emailStats struct {
			Sent24h          int     `json:"sent_24h"`
			Quota            int     `json:"quota"`
			Status           string  `json:"status"`
			BounceRate       float64 `json:"bounce_rate"`
			ComplaintRate    float64 `json:"complaint_rate"`
			// Legacy fields kept for backward compat
			Sent             int     `json:"sent"`
			Delivered        int     `json:"delivered"`
			Bounces          int     `json:"bounces"`
			Complaints       int     `json:"complaints"`
		}

		var stats emailStats
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/stats", orgID, projectID)
		if err := apiClient.Get(path, &stats); err != nil {
			return err
		}

		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(stats)
		}

		// Derive values from whichever fields the API returns
		sent24h := stats.Sent24h
		if sent24h == 0 {
			sent24h = stats.Sent
		}
		quota := stats.Quota
		status := stats.Status
		if status == "" {
			status = "active"
		}
		bounceRate := stats.BounceRate
		if bounceRate == 0 && stats.Sent > 0 {
			bounceRate = float64(stats.Bounces) / float64(stats.Sent) * 100.0
		}
		complaintRate := stats.ComplaintRate
		if complaintRate == 0 && stats.Sent > 0 {
			complaintRate = float64(stats.Complaints) / float64(stats.Sent) * 100.0
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SENT 24H\tQUOTA\tSTATUS\tBOUNCE RATE\tCOMPLAINT RATE")
		quotaStr := "—"
		if quota > 0 {
			quotaStr = fmt.Sprintf("%d", quota)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%.2f%%\t%.2f%%\n",
			sent24h, quotaStr, status, bounceRate, complaintRate)
		return w.Flush()
	},
}

// ── email dns ────────────────────────────────────────────────────────────────

type emailDNSRecord struct {
	Type   string `json:"type"`
	Host   string `json:"host"`
	Value  string `json:"value"`
	Status string `json:"status"`
}

type emailDNSResponse struct {
	Records            []emailDNSRecord `json:"records"`
	VerificationStatus string           `json:"verification_status"`
	DKIMStatus         string           `json:"dkim_status"`
}

var emailDNSCmd = &cobra.Command{
	Use:   "dns",
	Short: "Show DNS records required to verify email sending domain",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		var resp emailDNSResponse
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/dns-records", orgID, projectID)
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TYPE\tHOST\tVALUE\tSTATUS")
		for _, r := range resp.Records {
			value := r.Value
			if len(value) > 60 {
				value = value[:57] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Type, r.Host, value, r.Status)
		}
		if err := w.Flush(); err != nil {
			return err
		}

		verificationStatus := resp.VerificationStatus
		if verificationStatus == "" {
			verificationStatus = "pending"
		}
		dkimStatus := resp.DKIMStatus
		if dkimStatus == "" {
			dkimStatus = "pending"
		}
		fmt.Printf("\nVerification status: %s\n", verificationStatus)
		fmt.Printf("DKIM status:         %s\n", dkimStatus)
		return nil
	},
}

// ── email send ───────────────────────────────────────────────────────────────

var emailSendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send an email via SES",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		subject, _ := cmd.Flags().GetString("subject")
		bodyText, _ := cmd.Flags().GetString("body")
		htmlRaw, _ := cmd.Flags().GetString("html")

		if from == "" {
			return fmt.Errorf("--from is required")
		}
		if to == "" {
			return fmt.Errorf("--to is required")
		}
		if subject == "" {
			return fmt.Errorf("--subject is required")
		}

		// If --html starts with @, treat remainder as a file path
		htmlBody := htmlRaw
		if strings.HasPrefix(htmlRaw, "@") {
			filePath := htmlRaw[1:]
			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("reading HTML file %s: %w", filePath, err)
			}
			htmlBody = string(data)
		}

		payload := map[string]string{
			"from":    from,
			"to":      to,
			"subject": subject,
			"text":    bodyText,
			"html":    htmlBody,
		}

		var resp map[string]string
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/send", orgID, projectID)
		if err := apiClient.Post(path, payload, &resp); err != nil {
			return err
		}

		msgID := resp["message_id"]
		if msgID == "" {
			msgID = resp["id"]
		}
		fmt.Printf("Sent! Message ID: %s\n", msgID)
		return nil
	},
}

// ── email logs ───────────────────────────────────────────────────────────────

type emailLogItem struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type emailLogsResponse struct {
	Data []emailLogItem `json:"data"`
}

var emailLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View sent email logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/logs?limit=%d", orgID, projectID, limit)
		var resp emailLogsResponse
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "FROM\tTO\tSUBJECT\tSTATUS\tTIME")
		for _, l := range resp.Data {
			t := l.CreatedAt
			if len(t) > 19 {
				t = t[:19]
			}
			subject := l.Subject
			if len(subject) > 40 {
				subject = subject[:37] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", l.From, l.To, subject, l.Status, t)
		}
		return w.Flush()
	},
}

// ── email suppressions ───────────────────────────────────────────────────────

var emailSuppressionsCmd = &cobra.Command{
	Use:   "suppressions",
	Short: "Manage email suppression list",
}

type suppressionItem struct {
	Email        string `json:"email"`
	Reason       string `json:"reason"`
	SuppressedAt string `json:"suppressed_at"`
}

type suppressionsResponse struct {
	Data []suppressionItem `json:"data"`
}

var emailSuppressionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List suppressed email addresses",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/suppressions", orgID, projectID)
		var resp suppressionsResponse
		if err := apiClient.Get(path, &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "EMAIL\tREASON\tSUPPRESSED AT")
		for _, s := range resp.Data {
			t := s.SuppressedAt
			if len(t) > 19 {
				t = t[:19]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", s.Email, s.Reason, t)
		}
		return w.Flush()
	},
}

var emailSuppressionsRemoveCmd = &cobra.Command{
	Use:   "remove EMAIL",
	Short: "Remove an email address from the suppression list",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		email := args[0]

		// URL-encode the email to handle + and @ characters safely
		encodedEmail := url.PathEscape(email)
		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/email/suppressions/%s", orgID, projectID, encodedEmail)
		if err := apiClient.Delete(path); err != nil {
			return err
		}

		fmt.Printf("Removed %s from suppression list.\n", email)
		return nil
	},
}

// ── init ─────────────────────────────────────────────────────────────────────

func init() {
	// setup
	emailSetupCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailSetupCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	emailSetupCmd.Flags().String("domain", "", "Email domain to configure")

	// test
	emailTestCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailTestCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	emailTestCmd.Flags().String("to", "", "Recipient email address")

	// stats
	emailStatsCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailStatsCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")

	// dns
	emailDNSCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailDNSCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")

	// send
	emailSendCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailSendCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	emailSendCmd.Flags().String("from", "", "Sender email address (required)")
	emailSendCmd.Flags().String("to", "", "Recipient email address (required)")
	emailSendCmd.Flags().String("subject", "", "Email subject (required)")
	emailSendCmd.Flags().String("body", "", "Plain-text email body")
	emailSendCmd.Flags().String("html", "", "HTML email body, or @path/to/file.html to read from file")

	// logs
	emailLogsCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailLogsCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	emailLogsCmd.Flags().Int("limit", 20, "Maximum number of log entries to show")

	// suppressions list
	emailSuppressionsListCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailSuppressionsListCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")

	// suppressions remove
	emailSuppressionsRemoveCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	emailSuppressionsRemoveCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")

	emailSuppressionsCmd.AddCommand(emailSuppressionsListCmd, emailSuppressionsRemoveCmd)

	emailCmd.AddCommand(
		emailSetupCmd,
		emailTestCmd,
		emailStatsCmd,
		emailDNSCmd,
		emailSendCmd,
		emailLogsCmd,
		emailSuppressionsCmd,
	)
	rootCmd.AddCommand(emailCmd)
}
