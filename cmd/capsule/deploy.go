package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
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

var stdinReader = bufio.NewReader(os.Stdin)

func prompt(label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := stdinReader.ReadString('\n')
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
	line, _ := stdinReader.ReadString('\n')
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

// ── project detection ────────────────────────────────────────────────────────

// detectProjectSettings infers deploy settings from files present in dir.
func detectProjectSettings(dir string) (*config.ProjectConfig, string) {
	pc := &config.ProjectConfig{}
	detectedLang := ""

	// Dockerfile — highest priority for docker type
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		pc.DeployType = "docker"
		pc.Port = 3000
		// Try to read EXPOSE from Dockerfile
		if data, err := os.ReadFile(filepath.Join(dir, "Dockerfile")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(strings.ToUpper(line), "EXPOSE ") {
					parts := strings.Fields(line)
					if len(parts) >= 2 {
						if p, err := strconv.Atoi(parts[1]); err == nil {
							pc.Port = p
						}
					}
					break
				}
			}
		}
		detectedLang = "Docker"
		return pc, detectedLang
	}

	// go.mod
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		pc.DeployType = "docker"
		pc.BuildCommand = "CGO_ENABLED=0 go build -o server ."
		pc.StartCommand = "./server"
		pc.Port = 3000
		detectedLang = "Go"
		return pc, detectedLang
	}

	// package.json
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Main    string            `json:"main"`
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal(data, &pkg)

		hasBuild := pkg.Scripts["build"] != ""
		hasStart := pkg.Scripts["start"] != ""
		hasMain := pkg.Main != ""

		if hasMain || hasStart {
			pc.DeployType = "docker"
			pc.BuildCommand = "npm install"
			startCmd := "node index.js"
			if pkg.Main != "" {
				startCmd = "node " + pkg.Main
			} else if pkg.Scripts["start"] != "" {
				startCmd = pkg.Scripts["start"]
			}
			pc.StartCommand = startCmd
			pc.Port = 3000
			detectedLang = "Node.js"
		} else if hasBuild {
			pc.DeployType = "static"
			pc.BuildCommand = pkg.Scripts["build"]
			pc.OutputDir = "dist"
			detectedLang = "Node.js (static)"
		} else {
			pc.DeployType = "docker"
			pc.BuildCommand = "npm install"
			pc.StartCommand = "node index.js"
			pc.Port = 3000
			detectedLang = "Node.js"
		}
		return pc, detectedLang
	}

	// requirements.txt
	if _, err := os.Stat(filepath.Join(dir, "requirements.txt")); err == nil {
		pc.DeployType = "docker"
		pc.BuildCommand = "pip install -r requirements.txt"
		pc.StartCommand = "python app.py"
		pc.Port = 3000
		detectedLang = "Python"
		return pc, detectedLang
	}

	// index.html (no package.json)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		pc.DeployType = "static"
		pc.OutputDir = "."
		detectedLang = "Static HTML"
		return pc, detectedLang
	}

	// Default fallback
	pc.DeployType = "docker"
	pc.Port = 3000
	detectedLang = "Unknown"
	return pc, detectedLang
}

// deployTypeLabel returns a human-readable deploy type label.
func deployTypeLabel(dt string) string {
	switch dt {
	case "docker":
		return "Docker (24/7 container)"
	case "lambda":
		return "Lambda (serverless)"
	case "static":
		return "Static (CDN/S3)"
	default:
		return dt
	}
}

// askDeployType prompts user to pick a deploy type interactively.
func askDeployType() string {
	fmt.Println("? Deploy type:")
	fmt.Println("  1. Docker   — 24/7 container, always running")
	fmt.Println("  2. Lambda   — Serverless, runs on demand (AWS Lambda)")
	fmt.Println("  3. Static   — Static files served from CDN (S3)")
	choice := prompt("  Enter number", "1")
	switch choice {
	case "2":
		return "lambda"
	case "3":
		return "static"
	default:
		return "docker"
	}
}

// overrideSettings asks the user to override each detected setting.
func overrideSettings(pc *config.ProjectConfig) {
	dt := askDeployType()
	pc.DeployType = dt

	switch dt {
	case "docker":
		pc.BuildCommand = prompt("? Build command", pc.BuildCommand)
		pc.StartCommand = prompt("? Start command", pc.StartCommand)
		portStr := prompt("? Port", strconv.Itoa(pc.Port))
		if p, err := strconv.Atoi(portStr); err == nil {
			pc.Port = p
		}
	case "lambda":
		pc.BuildCommand = prompt("? Build command", pc.BuildCommand)
		pc.StartCommand = prompt("? Handler / start command", pc.StartCommand)
	case "static":
		pc.BuildCommand = prompt("? Build command", pc.BuildCommand)
		pc.OutputDir = prompt("? Output directory", pc.OutputDir)
	}
}

// printDetectedSettings prints a summary of auto-detected settings.
func printDetectedSettings(lang string, pc *config.ProjectConfig) {
	fmt.Printf("\nAuto-detected Project Settings (%s):\n", lang)
	fmt.Printf("  - Deploy type: %s\n", deployTypeLabel(pc.DeployType))
	if pc.BuildCommand != "" {
		fmt.Printf("  - Build command: %s\n", pc.BuildCommand)
	}
	if pc.StartCommand != "" {
		fmt.Printf("  - Start command: %s\n", pc.StartCommand)
	}
	if pc.Port > 0 && pc.DeployType == "docker" {
		fmt.Printf("  - Port: %d\n", pc.Port)
	}
	if pc.OutputDir != "" {
		fmt.Printf("  - Output dir: %s\n", pc.OutputDir)
	}
	fmt.Println()
}

// ── setup flow ───────────────────────────────────────────────────────────────

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

// runSetupFlow runs the full interactive setup (Vercel-style) and returns a ProjectConfig.
func runSetupFlow(cwd string) (*config.ProjectConfig, error) {
	// Get directory basename for display
	dirName := filepath.Base(cwd)

	if !confirm(fmt.Sprintf("? Set up and deploy %q?", dirName), true) {
		return nil, fmt.Errorf("aborted")
	}
	fmt.Println()

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
		fmt.Printf("  %d. %s\n", i+1, o.Name)
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
		var createErr error
		proj, createErr = createProjectInteractive(org.ID)
		if createErr != nil {
			return nil, createErr
		}
	} else {
		fmt.Println("? Link to existing project?")
		for i, p := range projResp.Data {
			fmt.Printf("  %d. %s\n", i+1, p.Name)
		}
		fmt.Printf("  %d. + Create new project\n", len(projResp.Data)+1)
		choice := prompt("  Enter number", "1")
		n, _ := strconv.Atoi(choice)
		if n == len(projResp.Data)+1 {
			var createErr error
			proj, createErr = createProjectInteractive(org.ID)
			if createErr != nil {
				return nil, createErr
			}
		} else {
			if n < 1 || n > len(projResp.Data) {
				n = 1
			}
			proj = projResp.Data[n-1]
		}
	}
	fmt.Printf("  ✓ %s\n\n", proj.Name)

	// 3. Auto-detect project settings
	detected, lang := detectProjectSettings(cwd)
	printDetectedSettings(lang, detected)

	if confirm("? Override these settings?", false) {
		overrideSettings(detected)
		fmt.Println()
	}

	pc := &config.ProjectConfig{
		OrgID:       org.ID,
		OrgName:     org.Name,
		ProjectID:   proj.ID,
		ProjectName: proj.Name,
		DeployType:  detected.DeployType,
		Port:        detected.Port,
		BuildCommand: detected.BuildCommand,
		StartCommand: detected.StartCommand,
		OutputDir:   detected.OutputDir,
	}

	// 4. Save .capsule.json
	if confirm("? Save settings to .capsule.json?", true) {
		if err := config.SaveProjectConfig(cwd, pc); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save .capsule.json: %v\n", err)
		} else {
			fmt.Println("  ✓ Saved — .capsule.json created\n")
		}
	}

	return pc, nil
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
	return pollDeploymentWithTimeout(orgID, projectID, deployID, 10*time.Minute)
}

func pollDeploymentWithTimeout(orgID, projectID, deployID string, timeout time.Duration) error {
	path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments/%s", orgID, projectID, deployID)
	lastStatus := ""
	start := time.Now()
	dots := 0

	terminal := []string{"success", "failed", "cancelled", "error", "timeout"}
	isTerminal := func(s string) bool {
		for _, t := range terminal {
			if s == t {
				return true
			}
		}
		return false
	}

	for {
		if time.Since(start) > timeout {
			fmt.Println()
			fmt.Println("Timed out waiting for deployment (use --timeout to extend)")
			os.Exit(1)
		}

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
			fmt.Printf("\n✅  %s  %s\n", d.Status, elapsed)
			// Fetch project info for URL + domain hints
			var projInfo struct {
				Slug       string `json:"slug"`
				DeployType string `json:"deploy_type"`
			}
			projPath := fmt.Sprintf("/api/v1/orgs/%s/projects/%s", orgID, projectID)
			if err2 := apiClient.Get(projPath, &projInfo); err2 == nil && projInfo.Slug != "" {
				appURL := fmt.Sprintf("https://%s.apps.tumi-ai.com", projInfo.Slug)
				fmt.Println()
				fmt.Printf("  🌐  %s\n", appURL)
				fmt.Println()
				fmt.Println("  Custom domain (optional):")
				fmt.Printf("    1. Point your DNS:  CNAME  your-domain.com  →  %s.apps.tumi-ai.com\n", projInfo.Slug)
				fmt.Printf("    2. capsule domains add --org %s --project %s --domain your-domain.com\n", orgID, projectID)
				fmt.Printf("    3. capsule domains verify --org %s --project %s --domain-id <id>\n", orgID, projectID)
			}
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
		yes, _ := cmd.Flags().GetBool("yes")

		// Confirm deploy directory
		if !yes && !confirm(fmt.Sprintf("? Deploy from %q?", cwd), true) {
			return fmt.Errorf("aborted")
		}
		fmt.Println()

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
				// Interactive setup flow
				pc, err = runSetupFlow(cwd)
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
			"version":        "cli",
			"git_sha":        sha,
			"branch":         branch,
			"source_key":     sourceKey,
			"build_strategy": pc.DeployType,
		}
		var resp deployment
		if err := apiClient.Post(
			fmt.Sprintf("/api/v1/orgs/%s/projects/%s/deployments", pc.OrgID, pc.ProjectID),
			body, &resp,
		); err != nil {
			return err
		}

		fmt.Printf("⏳  queued  0s\n")

		// Parse --timeout flag (default 10m)
		timeoutStr, _ := cmd.Flags().GetString("timeout")
		pollTimeout, err := time.ParseDuration(timeoutStr)
		if err != nil || pollTimeout <= 0 {
			pollTimeout = 10 * time.Minute
		}

		// Poll real-time
		return pollDeploymentWithTimeout(pc.OrgID, pc.ProjectID, resp.ID, pollTimeout)
	},
}

var linkCmd = &cobra.Command{
	Use:   "link",
	Short: "Link current directory to a Capsule project",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, _ := os.Getwd()
		_, err := runSetupFlow(cwd)
		return err
	},
}

func init() {
	deployCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	deployCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	deployCmd.Flags().String("sha", "", "Git SHA override")
	deployCmd.Flags().BoolP("yes", "y", false, "Skip all confirmation prompts")
	deployCmd.Flags().String("timeout", "10m", "Maximum time to wait for deployment (e.g. 5m, 30m, 1h)")
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(linkCmd)
}
