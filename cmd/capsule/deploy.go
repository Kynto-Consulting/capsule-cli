package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

func formatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
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

// ── source archive ───────────────────────────────────────────────────────────

// createSourceArchive tries git archive first, then falls back to manual walk.
func createSourceArchive(dir string) ([]byte, error) {
	cmd := exec.Command("git", "archive", "--format=tar.gz", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		return out, nil
	}
	return createTarGzManual(dir)
}

// loadGitignorePatterns reads .gitignore in dir and returns non-comment patterns.
func loadGitignorePatterns(dir string) []string {
	f, err := os.Open(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// matchesGitignore returns true if name matches any of the given patterns.
func matchesGitignore(patterns []string, relPath string) bool {
	base := filepath.Base(relPath)
	for _, pat := range patterns {
		// Try full relative path match
		if matched, _ := filepath.Match(pat, relPath); matched {
			return true
		}
		// Try base name match
		if matched, _ := filepath.Match(pat, base); matched {
			return true
		}
		// Prefix directory match (e.g. "dist/")
		trimmed := strings.TrimSuffix(pat, "/")
		if strings.HasPrefix(relPath, trimmed+string(filepath.Separator)) || relPath == trimmed {
			return true
		}
	}
	return false
}

var skipDirs = map[string]bool{
	".git":         true,
	".svn":         true,
	"node_modules": true,
	"vendor":       true,
}

func createTarGzManual(dir string) ([]byte, error) {
	patterns := loadGitignorePatterns(dir)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return nil
		}

		// Normalise to forward slashes for matching
		relSlash := filepath.ToSlash(rel)

		// Skip hidden/vendor dirs
		if d.IsDir() {
			base := d.Name()
			if skipDirs[base] {
				return filepath.SkipDir
			}
			if strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
		}

		// Skip .gitignore matches
		if matchesGitignore(patterns, relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil // directories are implicit in tar
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		hdr := &tar.Header{
			Name:    relSlash,
			Size:    info.Size(),
			Mode:    int64(info.Mode()),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("walking directory: %w", err)
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("closing gzip: %w", err)
	}
	return buf.Bytes(), nil
}

// uploadToS3 performs a raw PUT to a presigned S3 URL (no auth header).
func uploadToS3(uploadURL string, data []byte) error {
	req, err := http.NewRequest("PUT", uploadURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating S3 request: %w", err)
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/x-tar")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("uploading to S3: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 upload failed %d: %s", resp.StatusCode, body)
	}
	return nil
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
	"queued":    "⏳",
	"building":  "🔨",
	"deploying": "🚀",
	"running":   "✅",
	"failed":    "❌",
	"cancelled": "🚫",
	"success":   "✅",
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

			// Fetch project name to populate config (best-effort)
			var projListResp struct {
				Data []projectItem `json:"data"`
			}
			if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/projects", orgFlag), &projListResp); err == nil {
				for _, p := range projListResp.Data {
					if p.ID == projFlag {
						pc.ProjectName = p.Name
						break
					}
				}
			}

			// Fetch org name (best-effort)
			var orgsResp struct {
				Data []orgItem `json:"data"`
			}
			if err := apiClient.Get("/api/v1/orgs", &orgsResp); err == nil {
				for _, o := range orgsResp.Data {
					if o.ID == orgFlag {
						pc.OrgName = o.Name
						break
					}
				}
			}

			// Save .capsule.json so future runs don't need flags
			if err := config.SaveProjectConfig(cwd, pc); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not save .capsule.json: %v\n", err)
			} else {
				fmt.Println("  ✓ Linked — .capsule.json created")
			}
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

		// --- Upload flow ---
		sourceKey := ""

		type uploadURLResp struct {
			UploadURL string `json:"upload_url"`
			SourceKey string `json:"source_key"`
		}
		var uploadResp uploadURLResp
		uploadErr := apiClient.Post(
			fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/upload-url", pc.OrgID, pc.ProjectID),
			map[string]string{},
			&uploadResp,
		)

		if uploadErr == nil && uploadResp.UploadURL != "" {
			fmt.Print("  Packaging source...")
			archive, archErr := createSourceArchive(cwd)
			if archErr != nil {
				fmt.Printf(" failed (%v), skipping upload\n", archErr)
			} else {
				fmt.Printf(" %s\n", formatBytes(len(archive)))
				fmt.Printf("  Uploading %s...\n", formatBytes(len(archive)))
				if s3Err := uploadToS3(uploadResp.UploadURL, archive); s3Err != nil {
					fmt.Fprintf(os.Stderr, "  Warning: upload failed (%v), deploying without source\n", s3Err)
				} else {
					sourceKey = uploadResp.SourceKey
					fmt.Println("  ✓ Source uploaded")
				}
			}
		} else if uploadErr != nil {
			// Backend doesn't support upload yet — proceed without source
			fmt.Println("  (source upload not supported by server, deploying via git SHA)")
		}

		// --- Trigger deployment ---
		fmt.Println("  Triggering deployment...")
		body := map[string]string{
			"version":    "cli",
			"git_sha":    sha,
			"branch":     branch,
			"source_key": sourceKey,
		}
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
