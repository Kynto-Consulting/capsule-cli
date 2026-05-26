package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var pricingCmd = &cobra.Command{
	Use:   "pricing",
	Short: "Calculate on-demand cost estimates for AWS resources",
}

var pricingEstimateCmd = &cobra.Command{
	Use:   "estimate",
	Short: "Estimate costs for RDS, EC2, S3, or Lambda",
	RunE: func(cmd *cobra.Command, args []string) error {
		resource, _ := cmd.Flags().GetString("resource")
		if resource == "" {
			return fmt.Errorf("--resource is required (rds, ec2, s3, lambda)")
		}

		var config map[string]any

		switch resource {
		case "rds":
			engine, _ := cmd.Flags().GetString("engine")
			class, _ := cmd.Flags().GetString("class")
			storage, _ := cmd.Flags().GetInt("storage")
			multiAZ, _ := cmd.Flags().GetBool("multi-az")
			config = map[string]any{
				"engine":         engine,
				"instance_class": class,
				"storage_gb":     storage,
				"multi_az":       multiAZ,
			}
		case "ec2":
			instanceType, _ := cmd.Flags().GetString("type")
			count, _ := cmd.Flags().GetInt("count")
			config = map[string]any{
				"instance_type": instanceType,
				"count":         count,
			}
		case "s3":
			storage, _ := cmd.Flags().GetInt("storage")
			requests, _ := cmd.Flags().GetInt("requests")
			config = map[string]any{
				"storage_gb": storage,
				"requests_k": requests,
			}
		case "lambda":
			requests, _ := cmd.Flags().GetInt("requests")
			duration, _ := cmd.Flags().GetInt("duration")
			config = map[string]any{
				"requests_m":      requests,
				"avg_duration_ms": duration,
			}
		default:
			return fmt.Errorf("invalid resource type: %s", resource)
		}

		body := map[string]any{
			"resource_type": resource,
			"config":        config,
		}

		type costItem struct {
			Name string  `json:"name"`
			Cost float64 `json:"cost"`
			Unit string  `json:"unit"`
		}
		type costEstimate struct {
			MonthlyUSD float64    `json:"monthly_usd"`
			AnnualUSD  float64    `json:"annual_usd"`
			Breakdown  []costItem `json:"breakdown"`
		}

		var resp costEstimate
		if err := apiClient.Post("/api/v1/pricing/estimate", body, &resp); err != nil {
			return err
		}

		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp)
		}

		fmt.Println("┌─ Cost Estimate ──────────────────────────────┐")
		fmt.Printf("│ Resource: %-34s │\n", strings.ToUpper(resource))
		fmt.Println("│                                              │")
		for _, item := range resp.Breakdown {
			fmt.Printf("│  %-14s $%6.2f/%s                  │\n", truncate(item.Name, 14), item.Cost, item.Unit)
		}
		fmt.Println("│  ─────────────────────────────               │")
		fmt.Printf("│  Total:        ~$%6.2f/month                 │\n", resp.MonthlyUSD)
		fmt.Printf("│  Annual:       ~$%6.2f/year                  │\n", resp.AnnualUSD)
		fmt.Println("└──────────────────────────────────────────────┘")

		return nil
	},
}

func truncate(str string, num int) string {
	if len(str) > num {
		return str[0:num-3] + "..."
	}
	return str
}

func init() {
	pricingEstimateCmd.Flags().String("resource", "", "Resource to estimate: rds, ec2, s3, lambda")
	
	// RDS flags
	pricingEstimateCmd.Flags().String("engine", "postgres", "RDS Database Engine (postgres, mysql)")
	pricingEstimateCmd.Flags().String("class", "db.t3.micro", "RDS instance class (e.g. db.t3.micro, db.t3.small)")
	pricingEstimateCmd.Flags().Int("storage", 20, "RDS storage size in GB")
	pricingEstimateCmd.Flags().Bool("multi-az", false, "Enable Multi-AZ replication")

	// EC2 flags
	pricingEstimateCmd.Flags().String("type", "t3.small", "EC2 Instance type (e.g. t3.small, t3.medium)")
	pricingEstimateCmd.Flags().Int("count", 1, "Number of EC2 replicas")

	// S3 / Lambda general flags
	pricingEstimateCmd.Flags().Int("requests", 1, "Requests count (K for S3, M for Lambda)")
	pricingEstimateCmd.Flags().Int("duration", 200, "Average Lambda function duration in ms")

	pricingCmd.AddCommand(pricingEstimateCmd)
	rootCmd.AddCommand(pricingCmd)
}
