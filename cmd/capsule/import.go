package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ── import command ────────────────────────────────────────────────────────────

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import a project from a zip archive",
	Long:  "Read a Capsule export zip and restore restorable resources (env vars) to the target project.",
	RunE: func(cmd *cobra.Command, args []string) error {
		inputPath, _ := cmd.Flags().GetString("input")
		if inputPath == "" {
			return fmt.Errorf("--input is required")
		}
		onlyEnvs, _ := cmd.Flags().GetBool("only-envs")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		yes, _ := cmd.Flags().GetBool("yes")

		// ── 1. Open zip ────────────────────────────────────────────────────
		zr, err := zip.OpenReader(inputPath)
		if err != nil {
			return fmt.Errorf("opening zip: %w", err)
		}
		defer zr.Close()

		// ── 2. Read manifest ───────────────────────────────────────────────
		manifestData, err := readZipFile(&zr.Reader, "manifest.json")
		if err != nil {
			return fmt.Errorf("reading manifest: %w", err)
		}
		var manifest exportManifest
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			return fmt.Errorf("parsing manifest: %w", err)
		}

		// ── 3. Print summary ───────────────────────────────────────────────
		fmt.Println()
		fmt.Println("Import summary")
		fmt.Println("─────────────────────────────────────────")

		exportedAt := manifest.ExportedAt
		if t, err := time.Parse(time.RFC3339, exportedAt); err == nil {
			exportedAt = t.Format("2006-01-02 15:04:05 UTC")
		}
		fmt.Printf("  Archive:    %s\n", inputPath)
		fmt.Printf("  Exported:   %s\n", exportedAt)
		fmt.Printf("  Project ID: %s\n", manifest.ProjectID)
		fmt.Printf("  Org ID:     %s\n", manifest.OrgID)
		fmt.Printf("  CLI version:%s\n", manifest.CapsuleCLIVersion)

		if len(manifest.Included) > 0 {
			fmt.Println()
			fmt.Println("  Included resources:")
			for _, r := range manifest.Included {
				fmt.Printf("    · %s\n", r)
			}
		}
		fmt.Println()

		// Determine target org/project: prefer flags, then manifest
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			// Fall back to manifest values
			orgID = manifest.OrgID
			projectID = manifest.ProjectID
			if orgID == "" || projectID == "" {
				return fmt.Errorf("could not determine target org/project: use --org and --project flags or run from a linked directory")
			}
			fmt.Printf("  Target:     org=%s  project=%s  (from manifest)\n\n", orgID, projectID)
		} else {
			fmt.Printf("  Target:     org=%s  project=%s\n\n", orgID, projectID)
		}

		// ── 4. Dry-run or confirm ──────────────────────────────────────────
		if dryRun {
			fmt.Println("[dry-run] No changes will be made.")
			fmt.Println()
			printImportPlan(manifest.Included, onlyEnvs)
			return nil
		}

		if !yes {
			fmt.Print("Proceed with import? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		// ── 5. Warn about read-only resources ─────────────────────────────
		if !onlyEnvs {
			hasReadOnly := false
			readOnlyResources := []string{"capsule.json", "deployments.json", "databases.json", "domains.json"}
			included := sliceToSet(manifest.Included)
			for _, r := range readOnlyResources {
				if included[r] {
					hasReadOnly = true
					break
				}
			}
			if hasReadOnly {
				fmt.Println()
				fmt.Println("  Note: project config, databases, and domains are informational only.")
				fmt.Println("  They cannot be re-created by import. Only env vars will be restored.")
				fmt.Println()
			}
		}

		// ── 6. Restore env vars ────────────────────────────────────────────
		envData, err := readZipFile(&zr.Reader, "envvars.json")
		if err != nil {
			fmt.Println("  - envvars.json not found in archive, nothing to restore")
			return nil
		}

		// Parse — support both plain []envVarItem and {data:[...]} envelope
		envVars, err := parseEnvVars(envData)
		if err != nil {
			return fmt.Errorf("parsing envvars.json: %w", err)
		}

		if len(envVars) == 0 {
			fmt.Println("  No env vars found in archive.")
			return nil
		}

		fmt.Printf("Restoring %d env var(s)...\n\n", len(envVars))

		path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/envvars", orgID, projectID)
		restored := 0
		failed := 0
		for _, ev := range envVars {
			body := map[string]string{"key": ev.Key, "value": ev.Value}
			var resp map[string]interface{}
			if err := apiClient.Post(path, body, &resp); err != nil {
				fmt.Printf("  ✗ %s  (error: %v)\n", ev.Key, err)
				failed++
			} else {
				fmt.Printf("  ✓ %s\n", ev.Key)
				restored++
			}
		}

		fmt.Println()
		fmt.Printf("Import complete: %d restored, %d failed\n", restored, failed)
		if failed > 0 {
			return fmt.Errorf("%d env var(s) failed to import", failed)
		}
		return nil
	},
}

// ── helpers ───────────────────────────────────────────────────────────────────

// readZipFile returns the raw bytes of a named file within the zip.
func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}

// parseEnvVars handles both a plain JSON array and a {data:[...]} envelope.
func parseEnvVars(data []byte) ([]envVarItem, error) {
	var items []envVarItem
	if err := json.Unmarshal(data, &items); err == nil {
		return items, nil
	}
	var envelope struct {
		Data []envVarItem `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	return envelope.Data, nil
}

// sliceToSet converts a string slice to a presence map.
func sliceToSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// printImportPlan shows what a real run would do, for --dry-run.
func printImportPlan(included []string, onlyEnvs bool) {
	fmt.Println("What would happen:")
	set := sliceToSet(included)

	if set["envvars.json"] {
		fmt.Println("  · restore env vars from envvars.json")
	} else {
		fmt.Println("  · (no envvars.json in archive — nothing to restore)")
	}

	if !onlyEnvs {
		readOnly := []string{"capsule.json", "deployments.json", "databases.json", "domains.json"}
		for _, r := range readOnly {
			if set[r] {
				fmt.Printf("  · %s — read-only, will not be restored\n", r)
			}
		}
	}
}

func init() {
	importCmd.Flags().StringP("input", "i", "", "Zip file to import (required)")
	importCmd.Flags().Bool("only-envs", false, "Only restore env vars")
	importCmd.Flags().Bool("dry-run", false, "Show what would be imported without making changes")
	importCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	importCmd.Flags().String("org", "", "Org ID (overrides .capsule.json and manifest)")
	importCmd.Flags().String("project", "", "Project ID (overrides .capsule.json and manifest)")

	rootCmd.AddCommand(importCmd)
}
