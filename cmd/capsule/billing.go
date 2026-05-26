package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type billingResponse struct {
	TotalSpend       float64 `json:"total_spend"`
	Currency         string  `json:"currency"`
	Period           string  `json:"period"`
	RemainingCredits float64 `json:"remaining_credits"`
	CreditExpiration string  `json:"credit_expiration"`
	ActiveResources  struct {
		AppServers   int `json:"app_servers"`
		RDSDatabases int `json:"rds_databases"`
		S3Buckets    int `json:"s3_buckets"`
		Domains      int `json:"custom_domains"`
	} `json:"active_resources"`
	Breakdown []struct {
		Service string  `json:"service"`
		Cost    float64 `json:"cost"`
		Details string  `json:"details"`
	} `json:"breakdown"`
}

var billingCmd = &cobra.Command{
	Use:   "billing",
	Short: "Show global AWS spend and remaining promotional credits",
	Long:  "Display real-time summaries of cloud resource costs and remaining active AWS credits.",
	RunE: func(cmd *cobra.Command, args []string) error {
		var resp billingResponse
		if err := apiClient.Get("/api/v1/aws/billing", &resp); err != nil {
			return fmt.Errorf("failed to retrieve billing summary: %w", err)
		}

		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp)
		}

		fmt.Println("☁️  AWS SPEND & CREDITS SUMMARY")
		fmt.Printf("----------------------------------------------------------------\n")
		fmt.Printf("Active Period:      %s\n", resp.Period)
		fmt.Printf("Monthly Spend:      $%.2f %s\n", resp.TotalSpend, resp.Currency)
		fmt.Printf("Remaining Credits:  $%.2f %s\n", resp.RemainingCredits, resp.Currency)
		fmt.Printf("Credit Expiration:  %s\n", resp.CreditExpiration)
		fmt.Printf("----------------------------------------------------------------\n")

		fmt.Println("\n📊 ACTIVE AWS RESOURCES:")
		fmt.Printf("  • App Servers (EC2/Lambda):  %d\n", resp.ActiveResources.AppServers)
		fmt.Printf("  • RDS Databases (Postgres):  %d\n", resp.ActiveResources.RDSDatabases)
		fmt.Printf("  • S3 Storage Buckets:        %d\n", resp.ActiveResources.S3Buckets)
		fmt.Printf("  • Custom Mapped Domains:     %d\n", resp.ActiveResources.Domains)

		fmt.Println("\n💸 DETAILED AWS SERVICE BREAKDOWN:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SERVICE\tESTIMATED COST\tDETAILS")
		fmt.Fprintln(w, "-------\t--------------\t-------")
		for _, b := range resp.Breakdown {
			fmt.Fprintf(w, "%s\t$%.2f\t%s\n", b.Service, b.Cost, b.Details)
		}
		w.Flush()
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(billingCmd)
}
