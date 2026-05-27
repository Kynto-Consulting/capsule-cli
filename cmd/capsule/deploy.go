package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// ── helpers ─────────────────────────────────────────────────────────────────

func prompt(label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	val := strings.TrimSpace(line)
	if val == "" {
		return defaultVal
	}
	return val
}

func confirm(label string, defaultYes bool) bool {
	def := "Y/n"
	if !defaultYes {
		def = "y/N"
	}
	fmt.Printf("%s [%s] ", label, def)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	val := strings.ToLower(strings.TrimSpace(line))
	if val == "" {
		return defaultYes
	}
	return val == "y" || val == "yes"
}

func gitSHA() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitBranch() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ── types ────────────────────────────────────────────────────────────────────

type orgItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type projectItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Status  string `json:"status"`
	Runtime string `json:"runtime"`
}

type deployment struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ── link flow ────────────────────────────────────────────────────────────────

func runLinkFlow(cwd string) (*config.ProjectConfig, error) {
	fmt.Println("\nNo project linked to this directory.")
	fmt.Println("Let's set one up.\n")

	// 1. List orgs
	var orgsResp struct {
		Data []orgItem `json:"data"`
	}
	if err := apiClient.Get("/api/v1/orgs", &orgsResp); err != nil {
		return nil, fmt.Errorf("fetching orgs: %w", err)
	}
	if len(orgsResp.Data) == 0 {
		return nil, fmt.Errorf("no organizations found — create one with: capsule orgs create")
	}

	fmt.Println("? Which organization?")
	for i, o := range orgsResp.Data {
		fmt.Printf("  %d. %s (%s)\n", i+1, o.Name, o.Slug)
	}
	orgChoice := prompt("  Enter number", "1")
	idx, _ := strconv.Atoi(orgChoice)
	if idx < 1 || idx > len(orgsResp.Data) {
		idx = 1
	}
	org := orgsResp.Data[idx-1]
	fmt.Printf("  ✓ %s\n\n", org.Name)

	// 2. List projects
	var projResp struct {
		Data []projectItem `json:"data"`
	}
	if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/projects", org.ID), &projResp); err != nil {
		return nil, fmt.Errorf("fetching projects: %w", err)
	}

	var proj projectItem

	if len(projResp.Data) == 0 {
		fmt.Println("No projects found in this org.")
		if !confirm("? Create a new project?", true) {
			return nil, fmt.Errorf("aborted")
		}
		proj, _ = createProjectInteractive(org.ID)
	} else {
		fmt.Println("? Which project?")
		for i, p := range projResp.Data {
			fmt.Printf("  %d. %s\n", i+1, p.Name)
		}
		fmt.Printf("  %d. + Create new project\n", len(projResp.Data)+1)
		choice := prompt("  Enter number", "1")
		n, _ := strconv.Atoi(choice)
		if n == len(projResp.Data)+1 {
			proj, _ = createProjectInteractive(org.ID)
		} else {
			if n < 1 || n > len(projResp.Data) {
				n = 1
			}
			proj = projResp.Data[n-1]
		}
	}
	fmt.Printf("  ✓ %s\n\n", proj.Name)

	pc := &config.ProjectConfig{
		OrgID:       org.ID,
		OrgName:     org.Name,
		ProjectID:   proj.ID,
		ProjectName: proj.Name,
	}

	// 3. Save .capsule.json
	if confirm("? Link this directory to "+proj.Name+"?", true) {
		if err := config.SaveProjectConfig(cwd, pc); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save .capsule.json: %v\n", err)
		} else {
			fmt.Println("  ✓ Linked — .capsule.json created\n")
		}
	}

	return pc, nil
}

func createProjectInteractive(orgID string) (projectItem, error) {
	name := prompt("? Project name", "")
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	slug = prompt("? Project slug", slug)
	runtime := prompt("? Runtime (node/go/python/rust)", "node")

	body := map[string]string{"name": name, "slug": slug, "runtime": runtime}
	var resp struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Slug    string `json:"slug"`
		Runtime string `json:"runtime"`
	}
	if err := apiClient.Post(fmt.Sprintf("/api/v1/orgs/%s/projects", orgID), body, &resp); err != nil {
		return projectItem{}, err
	}
	fmt.Printf("  ✓ Project created\n")
	return projectItem{ID: resp.ID, Name: resp.Name, Slug: resp.Slug, Runtime: resp.Runtime}, nil
}

// ── deploy polling ───────────────────────────────────────────────────────────

var statusEmoji = map[string]string{
	"queued":     "⏳",
	"building":   "🔨",
	"deploying":  "🚀",
	"running":    "✅",
	"failed":     "❌",
	"cancelled":  "🚫",
	"success":    "✅",
}

func pollDeployment(orgID, projectID, deployID string) error {
	path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/%s", orgID, projectID, deployID)
	lastStatus := ""
	start := time.Now()
	dots := 0

	terminal := []string{"running", "failed", "cancelled", "success"}
	isTerminal := func(s string) bool {
		for _, t := range terminal {
			if s == t {
				return true
			}
		}
		return false
	}

	for {
		var d deployment
		if err := apiClient.Get(path, &d); err != nil {
			return err
		}

		if d.Status != lastStatus {
			emoji := statusEmoji[d.Status]
			if emoji == "" {
				emoji = "·"
			}
			elapsed := time.Since(start).Round(time.Second)
			fmt.Printf("\r%s  %s  %s\n", emoji, d.Status, elapsed)
			lastStatus = d.Status
		} else {
			dots = (dots + 1) % 4
			fmt.Printf("\r  %s%s", lastStatus, strings.Repeat(".", dots+1)+"   ")
		}

		if isTerminal(d.Status) {
			elapsed := time.Since(start).Round(time.Second)
			if d.Status == "failed" {
				fmt.Printf("\nDeployment failed after %s. Check logs:\n", elapsed)
				fmt.Printf("  capsule deployments logs --org %s --project %s --id %s\n", orgID, projectID, deployID)
				return fmt.Errorf("deployment failed")
			}
			fmt.Printf("\nDeployment complete in %s\n", elapsed)
			fmt.Printf("ID: %s\n", deployID)
			return nil
		}

		time.Sleep(2 * time.Second)
	}
}

// ── commands ──────────────────────────────────────────────────────────────────

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy current project",
	Long:  "Deploy the project linked to the current directory. Runs interactive setup if not yet linked.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, _ := os.Getwd()

		// Resolve project config
		orgFlag, _ := cmd.Flags().GetString("org")
		projFlag, _ := cmd.Flags().GetString("project")

		var pc *config.ProjectConfig

		if orgFlag != "" && projFlag != "" {
			pc = &config.ProjectConfig{OrgID: orgFlag, ProjectID: projFlag}
		} else {
			found, _, err := config.FindProjectConfig(cwd)
			if err == nil {
				pc = found
			} else {
				// Interactive link flow
				pc, err = runLinkFlow(cwd)
				if err != nil {
					return err
				}
			}
		}

		// Git info
		sha := gitSHA()
		branch := gitBranch()

		// Show what we're deploying
		name := pc.ProjectName
		if name == "" {
			name = pc.ProjectID
		}
		fmt.Printf("Deploying %s", name)
		if branch != "" {
			fmt.Printf(" (%s)", branch)
		}
		if sha != "" {
			fmt.Printf(" @ %s", sha)
		}
		fmt.Println()
		fmt.Println()

		// Trigger
		body := map[string]string{"version": "cli", "git_sha": sha, "branch": branch}
		var resp deployment
		if err := apiClient.Post(
			fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments", pc.OrgID, pc.ProjectID),
			body, &resp,
		); err != nil {
			return err
		}

		fmt.Printf("⏳  queued  0s\n")

		// Poll real-time
		return pollDeployment(pc.OrgID, pc.ProjectID, resp.ID)
	},
}

var linkCmd = &cobra.Command{
	Use:   "link",
	Short: "Link current directory to a Capsule project",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, _ := os.Getwd()
		_, err := runLinkFlow(cwd)
		return err
	},
}

func init() {
	deployCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	deployCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	deployCmd.Flags().String("sha", "", "Git SHA override")
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(linkCmd)
}
