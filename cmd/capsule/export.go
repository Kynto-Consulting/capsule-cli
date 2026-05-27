package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// ── manifest ─────────────────────────────────────────────────────────────────

type exportManifest struct {
	ExportedAt       string   `json:"exported_at"`
	ProjectID        string   `json:"project_id"`
	OrgID            string   `json:"org_id"`
	Included         []string `json:"included"`
	CapsuleCLIVersion string  `json:"capsule_cli_version"`
}

// ── export command ────────────────────────────────────────────────────────────

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export a project to a zip archive",
	Long:  "Fetch project resources from the API and bundle them into a portable zip file.",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}

		outputPath, _ := cmd.Flags().GetString("output")
		noLogs, _ := cmd.Flags().GetBool("no-logs")
		noEnvs, _ := cmd.Flags().GetBool("no-envs")
		noDeployments, _ := cmd.Flags().GetBool("no-deployments")
		maxLogs, _ := cmd.Flags().GetInt("max-logs")

		// Determine output file name
		if outputPath == "" {
			// Try to get project slug from the API for a nice default name
			slug := projectID
			var projData map[string]interface{}
			if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/projects/%s", orgID, projectID), &projData); err == nil {
				if s, ok := projData["slug"].(string); ok && s != "" {
					slug = s
				}
			}
			ts := time.Now().UTC().Format("20060102-150405")
			outputPath = fmt.Sprintf("%s-%s.zip", slug, ts)
		}

		// Open zip writer
		f, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()

		zw := zip.NewWriter(f)
		defer zw.Close()

		included := []string{}

		// ── helper: fetch JSON and write into zip ──────────────────────────
		addJSON := func(zipName, apiPath string) ([]byte, error) {
			var raw json.RawMessage
			if err := apiClient.Get(apiPath, &raw); err != nil {
				return nil, fmt.Errorf("fetching %s: %w", zipName, err)
			}
			data, err := json.MarshalIndent(raw, "", "  ")
			if err != nil {
				return nil, fmt.Errorf("marshaling %s: %w", zipName, err)
			}
			w, err := zw.Create(zipName)
			if err != nil {
				return nil, fmt.Errorf("creating zip entry %s: %w", zipName, err)
			}
			if _, err := w.Write(data); err != nil {
				return nil, fmt.Errorf("writing %s: %w", zipName, err)
			}
			included = append(included, zipName)
			return data, nil
		}

		fmt.Printf("Exporting project %s → %s\n\n", projectID, outputPath)

		// 1. capsule.json — project metadata
		projData, err := addJSON("capsule.json", fmt.Sprintf("/api/v1/orgs/%s/projects/%s", orgID, projectID))
		if err != nil {
			return err
		}
		// Extract name for a nicer label
		var proj struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(projData, &proj)
		label := proj.Name
		if label == "" {
			label = projectID
		}
		fmt.Printf("  ✓ capsule.json  (%s)\n", label)

		// 2. envvars.json
		if !noEnvs {
			data, err := addJSON("envvars.json", fmt.Sprintf("/api/v1/orgs/%s/projects/%s/envvars", orgID, projectID))
			if err != nil {
				return err
			}
			// Count vars — try array or {data:[...]}
			count := countItems(data)
			if count >= 0 {
				fmt.Printf("  ✓ envvars.json  (%d vars)\n", count)
			} else {
				fmt.Println("  ✓ envvars.json")
			}
		} else {
			fmt.Println("  - envvars.json  (skipped)")
		}

		// 3. deployments.json
		if !noDeployments {
			data, err := addJSON("deployments.json", fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments", orgID, projectID))
			if err != nil {
				return err
			}
			count := countItems(data)
			if count >= 0 {
				fmt.Printf("  ✓ deployments.json  (%d entries)\n", count)
			} else {
				fmt.Println("  ✓ deployments.json")
			}
		} else {
			fmt.Println("  - deployments.json  (skipped)")
		}

		// 4–7. logs/*
		if !noLogs {
			logSources := []string{"runtime", "lambda", "storage", "cron"}
			for _, src := range logSources {
				zipName := fmt.Sprintf("logs/%s.json", src)
				apiPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/logs/%s?limit=%d", orgID, projectID, src, maxLogs)
				data, err := addJSON(zipName, apiPath)
				if err != nil {
					// Non-fatal: some log sources may not exist for all project types
					fmt.Printf("  ! %s  (skipped: %v)\n", zipName, err)
					continue
				}
				count := countItems(data)
				if count >= 0 {
					fmt.Printf("  ✓ %s  (%d entries)\n", zipName, count)
				} else {
					fmt.Printf("  ✓ %s\n", zipName)
				}
			}
		} else {
			fmt.Println("  - logs/*  (skipped)")
		}

		// 8. databases.json
		if data, err := addJSON("databases.json", fmt.Sprintf("/api/v1/orgs/%s/projects/%s/databases", orgID, projectID)); err != nil {
			fmt.Printf("  ! databases.json  (skipped: %v)\n", err)
		} else {
			count := countItems(data)
			if count >= 0 {
				fmt.Printf("  ✓ databases.json  (%d entries)\n", count)
			} else {
				fmt.Println("  ✓ databases.json")
			}
		}

		// 9. domains.json
		if data, err := addJSON("domains.json", fmt.Sprintf("/api/v1/orgs/%s/projects/%s/domains", orgID, projectID)); err != nil {
			fmt.Printf("  ! domains.json  (skipped: %v)\n", err)
		} else {
			count := countItems(data)
			if count >= 0 {
				fmt.Printf("  ✓ domains.json  (%d entries)\n", count)
			} else {
				fmt.Println("  ✓ domains.json")
			}
		}

		// 10. manifest.json
		manifest := exportManifest{
			ExportedAt:        time.Now().UTC().Format(time.RFC3339),
			ProjectID:         projectID,
			OrgID:             orgID,
			Included:          included,
			CapsuleCLIVersion: version,
		}
		manifestData, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling manifest: %w", err)
		}
		mw, err := zw.Create("manifest.json")
		if err != nil {
			return fmt.Errorf("creating manifest zip entry: %w", err)
		}
		if _, err := mw.Write(manifestData); err != nil {
			return fmt.Errorf("writing manifest: %w", err)
		}
		fmt.Println("  ✓ manifest.json")

		// Flush zip before printing final message
		if err := zw.Close(); err != nil {
			return fmt.Errorf("finalizing zip: %w", err)
		}

		fi, _ := f.Stat()
		sizeKB := int64(0)
		if fi != nil {
			sizeKB = fi.Size() / 1024
		}

		fmt.Printf("\nExport complete: %s", outputPath)
		if sizeKB > 0 {
			fmt.Printf("  (%d KB)", sizeKB)
		}
		fmt.Println()
		return nil
	},
}

// countItems returns the number of items in a JSON array or {data:[...]} envelope.
// Returns -1 if the shape is unrecognised.
func countItems(data []byte) int {
	// Try direct array
	var arr []json.RawMessage
	if json.Unmarshal(data, &arr) == nil {
		return len(arr)
	}
	// Try {data:[...]} envelope
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) == nil && envelope.Data != nil {
		return len(envelope.Data)
	}
	return -1
}

func init() {
	exportCmd.Flags().StringP("output", "o", "", "Output zip file path (default: {slug}-{timestamp}.zip)")
	exportCmd.Flags().Bool("no-logs", false, "Skip execution logs")
	exportCmd.Flags().Bool("no-envs", false, "Skip env vars")
	exportCmd.Flags().Bool("no-deployments", false, "Skip deployment history")
	exportCmd.Flags().Int("max-logs", 500, "Max log entries per source")
	exportCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	exportCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")

	rootCmd.AddCommand(exportCmd)
}
