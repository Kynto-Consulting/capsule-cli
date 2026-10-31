package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// ── stack detection for CI ───────────────────────────────────────────────────

type ciStack struct {
	Name  string   // node-npm, node-pnpm, node-yarn, node-bun, go, python, other
	Setup []string // YAML step blocks (already indented by 6 spaces, no trailing newline)
	Run   []string // YAML step blocks for install/lint/test/build
	Notes []string // human-readable notes printed to the user
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// join builds "dir/name" using forward slashes (WASM/Windows friendly).
func join(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

func step(name string, lines ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "      - name: %s\n", name)
	for i, l := range lines {
		b.WriteString("        " + l)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// detectCIStack inspects dir (the app root) and returns CI steps for it.
// sub is the app path relative to the repository root ("" or "." for the root).
func detectCIStack(dir, sub, requested string) ciStack {
	p := func(n string) string { return join(dir, n) }
	rel := func(n string) string { return join(sub, n) }

	kind := requested
	if kind == "" || kind == "auto" {
		switch {
		case fileExists(p("package.json")):
			kind = "node"
		case fileExists(p("go.mod")):
			kind = "go"
		case fileExists(p("pyproject.toml")) || fileExists(p("requirements.txt")) || fileExists(p("setup.py")):
			kind = "python"
		default:
			kind = "other"
		}
	}

	switch kind {
	case "node", "node-npm", "node-pnpm", "node-yarn", "node-bun":
		return nodeCIStack(dir, rel, kind)
	case "go":
		return ciStack{
			Name: "go",
			Setup: []string{step("Set up Go",
				"uses: actions/setup-go@v5",
				"with:",
				"  go-version-file: "+rel("go.mod"),
				"  cache-dependency-path: "+rel("go.sum"))},
			Run: []string{
				step("Vet", "run: go vet ./..."),
				step("Test", "run: go test ./..."),
				step("Build", "run: go build ./..."),
			},
		}
	case "python":
		return pythonCIStack(dir, rel)
	}
	return ciStack{
		Name: "other",
		Run: []string{step("Placeholder",
			"run: echo \"capsule ci init found no known stack in this directory; add your install/test/build steps here\"")},
		Notes: []string{"No known stack detected (package.json, go.mod, pyproject.toml or requirements.txt); the ci job only has a placeholder step."},
	}
}

func nodeCIStack(dir string, rel func(string) string, kind string) ciStack {
	p := func(n string) string { return join(dir, n) }
	if kind == "node" {
		switch {
		case fileExists(p("pnpm-lock.yaml")):
			kind = "node-pnpm"
		case fileExists(p("bun.lockb")) || fileExists(p("bun.lock")):
			kind = "node-bun"
		case fileExists(p("yarn.lock")):
			kind = "node-yarn"
		default:
			kind = "node-npm"
		}
	}

	var pkg struct {
		PackageManager string            `json:"packageManager"`
		Scripts        map[string]string `json:"scripts"`
	}
	if data, err := os.ReadFile(p("package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}

	nodeVer := []string{"node-version: '20'"}
	for _, f := range []string{".nvmrc", ".node-version"} {
		if fileExists(p(f)) {
			nodeVer = []string{"node-version-file: " + rel(f)}
			break
		}
	}

	st := ciStack{Name: kind}
	var install, runner, lockfile, cache string
	switch kind {
	case "node-pnpm":
		pnpm := []string{"uses: pnpm/action-setup@v4"}
		if pkg.PackageManager == "" {
			pnpm = append(pnpm, "with:", "  version: 9")
			st.Notes = append(st.Notes, "pnpm version not pinned in package.json (packageManager); the workflow uses pnpm 9.")
		}
		st.Setup = append(st.Setup, step("Set up pnpm", pnpm...))
		install, runner, lockfile, cache = "pnpm install --frozen-lockfile", "pnpm run --if-present", "pnpm-lock.yaml", "pnpm"
	case "node-bun":
		st.Setup = append(st.Setup, step("Set up Bun", "uses: oven-sh/setup-bun@v2"))
		install, runner = "bun install --frozen-lockfile", "bun run --if-present"
	case "node-yarn":
		install, runner, lockfile, cache = "yarn install --frozen-lockfile", "yarn run --if-present", "yarn.lock", "yarn"
	default:
		install, runner, cache = "npm install", "npm run --if-present", "npm"
		if fileExists(p("package-lock.json")) {
			install, lockfile = "npm ci", "package-lock.json"
		}
	}

	if kind != "node-bun" {
		with := append([]string{"uses: actions/setup-node@v4", "with:"}, indentAll(nodeVer, "  ")...)
		if lockfile != "" && fileExists(p(lockfile)) {
			with = append(with, "  cache: "+cache, "  cache-dependency-path: "+rel(lockfile))
		}
		st.Setup = append(st.Setup, step("Set up Node.js", with...))
	}

	st.Run = append(st.Run, step("Install dependencies", "run: "+install))
	// Only scripts that exist in package.json; nothing is invented.
	found := 0
	for _, s := range []struct{ title, key string }{
		{"Lint", "lint"}, {"Type check", "typecheck"}, {"Type check", "type-check"}, {"Test", "test"}, {"Build", "build"},
	} {
		if pkg.Scripts[s.key] != "" {
			st.Run = append(st.Run, step(s.title, "run: "+runner+" "+s.key))
			found++
		}
	}
	if found == 0 {
		st.Notes = append(st.Notes, "package.json has no lint/typecheck/test/build scripts; the ci job only installs dependencies.")
	}
	return st
}

func pythonCIStack(dir string, rel func(string) string) ciStack {
	p := func(n string) string { return join(dir, n) }
	st := ciStack{Name: "python"}
	dep := ""
	switch {
	case fileExists(p("requirements.txt")):
		dep = rel("requirements.txt")
	case fileExists(p("pyproject.toml")):
		dep = rel("pyproject.toml")
	}
	setup := []string{"uses: actions/setup-python@v5", "with:", "  python-version: '3.12'"}
	if dep != "" {
		setup = append(setup, "  cache: pip", "  cache-dependency-path: "+dep)
	}
	st.Setup = append(st.Setup, step("Set up Python", setup...))

	var text string
	for _, f := range []string{"requirements.txt", "pyproject.toml"} {
		if d, err := os.ReadFile(p(f)); err == nil {
			text += strings.ToLower(string(d))
		}
	}
	switch {
	case fileExists(p("requirements.txt")):
		st.Run = append(st.Run, step("Install dependencies", "run: pip install -r requirements.txt"))
	case fileExists(p("pyproject.toml")):
		st.Run = append(st.Run, step("Install dependencies", "run: pip install ."))
	default:
		st.Notes = append(st.Notes, "No requirements.txt or pyproject.toml; no install step.")
	}
	if strings.Contains(text, "ruff") {
		st.Run = append(st.Run, step("Lint", "run: ruff check ."))
	}
	if strings.Contains(text, "pytest") || fileExists(p("tests")) || fileExists(p("test")) {
		if !strings.Contains(text, "pytest") {
			st.Run = append(st.Run, step("Install pytest", "run: pip install pytest"))
		}
		st.Run = append(st.Run, step("Test", "run: pytest"))
	}
	if len(st.Run) <= 1 {
		st.Run = append(st.Run, step("Byte-compile", "run: python -m compileall -q ."))
	}
	return st
}

func indentAll(lines []string, pre string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pre + l
	}
	return out
}

// ── workflow generation ──────────────────────────────────────────────────────

type ciOptions struct {
	Branch       string
	Dir          string // app path relative to the repo root, "" for root
	ActionRef    string // e.g. main or v1
	WorkflowName string
}

func generateWorkflow(st ciStack, o ciOptions) string {
	var b strings.Builder
	action := "kynto-consulting/capsule/actions/deploy@" + o.ActionRef
	wd := ""
	if o.Dir != "" && o.Dir != "." {
		wd = "          working-directory: " + o.Dir + "\n"
	}

	b.WriteString("# Generated by `capsule ci init`. Edit freely; re-run with --force to regenerate.\n")
	b.WriteString("#\n")
	b.WriteString("# Required repository secrets:   CAPSULE_TOKEN, CAPSULE_ORG_ID, CAPSULE_PROJECT_ID\n")
	b.WriteString("# Repository variables:          CAPSULE_API_URL (your Capsule API)\n")
	b.WriteString("# Optional variable (previews):  CAPSULE_PREVIEW_PROJECT_ID\n")
	fmt.Fprintf(&b, "name: %s\n\n", o.WorkflowName)
	fmt.Fprintf(&b, "on:\n  push:\n    branches: [%s]\n  pull_request:\n    branches: [%s]\n  workflow_dispatch:\n\n", o.Branch, o.Branch)
	b.WriteString("concurrency:\n  group: capsule-${{ github.workflow }}-${{ github.ref }}\n  cancel-in-progress: ${{ github.event_name == 'pull_request' }}\n\n")
	b.WriteString("permissions:\n  contents: read\n\n")
	b.WriteString("jobs:\n")

	// ci
	b.WriteString("  ci:\n    name: CI\n    runs-on: ubuntu-latest\n")
	if o.Dir != "" && o.Dir != "." {
		fmt.Fprintf(&b, "    defaults:\n      run:\n        working-directory: %s\n", o.Dir)
	}
	b.WriteString("    steps:\n      - uses: actions/checkout@v4\n")
	for _, s := range st.Setup {
		b.WriteString(s + "\n")
	}
	for _, s := range st.Run {
		b.WriteString(s + "\n")
	}

	// preview
	b.WriteString("\n  # Pull request previews. Capsule has no per-PR ephemeral environments: this job\n")
	b.WriteString("  # deploys the PR to a separate, long-lived preview PROJECT that you create once\n")
	b.WriteString("  # and reference in the CAPSULE_PREVIEW_PROJECT_ID variable. Without that variable\n")
	b.WriteString("  # (or for PRs from forks, which have no access to secrets) the job is skipped.\n")
	b.WriteString("  preview:\n    name: Preview\n    needs: ci\n    runs-on: ubuntu-latest\n")
	b.WriteString("    if: github.event_name == 'pull_request' && vars.CAPSULE_PREVIEW_PROJECT_ID != '' && github.event.pull_request.head.repo.full_name == github.repository\n")
	b.WriteString("    concurrency:\n      group: capsule-preview\n      cancel-in-progress: false\n")
	b.WriteString("    steps:\n      - uses: actions/checkout@v4\n")
	b.WriteString("      - name: Deploy preview\n        id: capsule\n        uses: " + action + "\n        with:\n")
	b.WriteString("          token: ${{ secrets.CAPSULE_TOKEN }}\n          org-id: ${{ secrets.CAPSULE_ORG_ID }}\n")
	b.WriteString("          project-id: ${{ vars.CAPSULE_PREVIEW_PROJECT_ID }}\n          api-url: ${{ vars.CAPSULE_API_URL }}\n")
	b.WriteString(wd)
	b.WriteString("          # env: |\n          #   APP_ENV=preview\n")
	b.WriteString("      - name: Comment preview URL\n        if: steps.capsule.outputs.url != ''\n        run: echo \"Preview URL ${{ steps.capsule.outputs.url }}\" >> \"$GITHUB_STEP_SUMMARY\"\n")

	// deploy
	b.WriteString("\n  deploy:\n    name: Deploy\n    needs: ci\n    runs-on: ubuntu-latest\n")
	fmt.Fprintf(&b, "    if: github.event_name == 'push' && github.ref == 'refs/heads/%s'\n", o.Branch)
	b.WriteString("    concurrency:\n      group: capsule-deploy\n      cancel-in-progress: false\n")
	b.WriteString("    steps:\n      - uses: actions/checkout@v4\n")
	b.WriteString("      - name: Deploy to Capsule\n        id: capsule\n        uses: " + action + "\n        with:\n")
	b.WriteString("          token: ${{ secrets.CAPSULE_TOKEN }}\n          org-id: ${{ secrets.CAPSULE_ORG_ID }}\n")
	b.WriteString("          project-id: ${{ secrets.CAPSULE_PROJECT_ID }}\n          api-url: ${{ vars.CAPSULE_API_URL }}\n")
	b.WriteString(wd)
	b.WriteString("          # Deploy-only variables (highest precedence: deploy > project > account):\n")
	b.WriteString("          # env: |\n          #   PUBLIC_API_URL=${{ vars.PUBLIC_API_URL }}\n")
	b.WriteString("      - name: Show URL\n        if: steps.capsule.outputs.url != ''\n        run: echo \"Deployed to ${{ steps.capsule.outputs.url }}\"\n")
	return b.String()
}

// ── gh integration ───────────────────────────────────────────────────────────

type postAction struct {
	Cmd   string   `json:"cmd"`
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
}

// hasGH: under WASM the Node wrapper probes for an authenticated gh and sets
// CAPSULE_HAS_GH=1; natively we look it up ourselves.
func hasGH() bool {
	if runtime.GOOS == "js" {
		return os.Getenv("CAPSULE_HAS_GH") == "1"
	}
	_, err := exec.LookPath("gh")
	return err == nil
}

// runGH runs `gh args` (stdin may carry a secret value). Under WASM, where
// os/exec is unavailable, the call is queued for the Node wrapper to execute
// after the CLI exits (file named by CAPSULE_POST_ACTIONS, mode 0600).
func runGH(args []string, stdin string) error {
	if runtime.GOOS == "js" {
		file := os.Getenv("CAPSULE_POST_ACTIONS")
		if file == "" {
			return fmt.Errorf("this launcher cannot run gh; set the secret manually")
		}
		var list []postAction
		if data, err := os.ReadFile(file); err == nil && len(data) > 0 {
			_ = json.Unmarshal(data, &list)
		}
		list = append(list, postAction{Cmd: "gh", Args: args, Stdin: stdin})
		data, _ := json.Marshal(list)
		return os.WriteFile(file, data, 0600)
	}
	c := exec.Command("gh", args...)
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// ── command ──────────────────────────────────────────────────────────────────

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "CI/CD helpers",
}

var ciInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a GitHub Actions workflow (CI, PR preview, deploy on main)",
	Long: `Detects the stack and writes .github/workflows/capsule.yml with:
  ci       install, lint, test and build (only steps that exist in the project)
  preview  on pull requests: deploys to a separate preview project when the
           CAPSULE_PREVIEW_PROJECT_ID variable is set, otherwise skipped
  deploy   on push to the main branch, after ci, using the Capsule deploy action

With --set-secrets (needs the gh CLI, authenticated, inside the repository) it also
creates a deploy-scoped token limited to the project and sets the CAPSULE_TOKEN,
CAPSULE_ORG_ID and CAPSULE_PROJECT_ID secrets and the CAPSULE_API_URL variable.`,
	Example: `  capsule ci init
  capsule ci init --stack node --working-directory apps/web
  capsule ci init --set-secrets --org <org-id> --project <project-id>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, _ := cmd.Flags().GetString("provider")
		if provider != "github" {
			return fmt.Errorf("unsupported --provider %q (only github is supported)", provider)
		}
		stack, _ := cmd.Flags().GetString("stack")
		switch stack {
		case "auto", "node", "node-npm", "node-pnpm", "node-yarn", "node-bun", "go", "python", "other":
		default:
			return fmt.Errorf("unsupported --stack %q (auto, node, node-pnpm, node-yarn, node-bun, go, python, other)", stack)
		}
		branch, _ := cmd.Flags().GetString("branch")
		sub, _ := cmd.Flags().GetString("working-directory")
		sub = strings.Trim(strings.ReplaceAll(sub, "\\", "/"), "/")
		if sub == "." {
			sub = ""
		}
		if strings.Contains(sub, "..") {
			return fmt.Errorf("--working-directory must be a path inside the repository")
		}
		actionRef, _ := cmd.Flags().GetString("action-ref")
		force, _ := cmd.Flags().GetBool("force")
		toStdout, _ := cmd.Flags().GetBool("print")
		setSecrets, _ := cmd.Flags().GetBool("set-secrets")

		root, _ := os.Getwd()
		if d := os.Getenv("CAPSULE_CWD"); d != "" {
			root = d
		}
		root = strings.ReplaceAll(root, "\\", "/")
		appDir := root
		if sub != "" {
			appDir = root + "/" + sub
		}

		st := detectCIStack(appDir, sub, stack)
		name := "Capsule CI/CD"
		file := "capsule.yml"
		if sub != "" {
			name = "Capsule CI/CD (" + sub + ")"
			file = "capsule-" + strings.ReplaceAll(path.Clean(sub), "/", "-") + ".yml"
		}
		yaml := generateWorkflow(st, ciOptions{Branch: branch, Dir: sub, ActionRef: actionRef, WorkflowName: name})

		if toStdout {
			fmt.Print(yaml)
			return nil
		}

		outDir := root + "/.github/workflows"
		outFile := outDir + "/" + file
		if fileExists(outFile) && !force {
			return fmt.Errorf("%s already exists (use --force to overwrite, or --print to preview)", outFile)
		}
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", outDir, err)
		}
		if err := os.WriteFile(outFile, []byte(yaml), 0644); err != nil {
			return fmt.Errorf("writing workflow: %w", err)
		}
		fmt.Printf("Detected stack: %s\n", st.Name)
		fmt.Printf("Wrote .github/workflows/%s\n", file)
		for _, n := range st.Notes {
			fmt.Printf("  note: %s\n", n)
		}
		fmt.Println("  note: PR previews need a separate preview project (variable CAPSULE_PREVIEW_PROJECT_ID); Capsule has no per-PR environments.")

		orgID, _ := cmd.Flags().GetString("org")
		projectID, _ := cmd.Flags().GetString("project")
		eo, ep := envOrgProject()
		if orgID == "" {
			orgID = eo
		}
		if projectID == "" {
			projectID = ep
		}
		if orgID == "" || projectID == "" {
			if pc, _, err := config.FindProjectConfig(root); err == nil {
				if orgID == "" {
					orgID = pc.OrgID
				}
				if projectID == "" {
					projectID = pc.ProjectID
				}
			}
		}

		if setSecrets {
			return ciSetSecrets(cmd, orgID, projectID)
		}

		fmt.Println("\nNext steps (or re-run with --set-secrets to automate 1-2 using gh):")
		fmt.Println("  1. capsule tokens create --name github-ci --scope deploy --project <project-id>")
		fmt.Println("  2. Repository settings > Secrets and variables > Actions:")
		fmt.Println("       secrets:   CAPSULE_TOKEN, CAPSULE_ORG_ID, CAPSULE_PROJECT_ID")
		fmt.Println("       variables: CAPSULE_API_URL")
		fmt.Println("     or: gh secret set CAPSULE_TOKEN; gh secret set CAPSULE_ORG_ID; gh secret set CAPSULE_PROJECT_ID; gh variable set CAPSULE_API_URL")
		if orgID != "" && projectID != "" {
			fmt.Printf("  org id: %s\n  project id: %s\n", orgID, projectID)
		}
		fmt.Println("  3. Commit and push .github/workflows/" + file)
		return nil
	},
}

func ciSetSecrets(cmd *cobra.Command, orgID, projectID string) error {
	if orgID == "" || projectID == "" {
		return fmt.Errorf("--set-secrets needs the org and project: pass --org/--project, set CAPSULE_ORG_ID/CAPSULE_PROJECT_ID or link with: capsule link")
	}
	if !hasGH() {
		fmt.Fprintln(os.Stderr, "\n--set-secrets: gh CLI not found or not authenticated (run: gh auth login). No token was created; follow the manual steps above.")
		return fmt.Errorf("gh unavailable")
	}
	if len(projectID) < 36 {
		if id, err := resolveIDPrefix("/api/v1/orgs/"+orgID+"/projects", "project", projectID); err == nil {
			projectID = id
		}
	}
	repoArgs := []string{}
	if r, _ := cmd.Flags().GetString("repo"); r != "" {
		repoArgs = []string{"--repo", r}
	}

	tokenName := "github-ci"
	if r, _ := cmd.Flags().GetString("repo"); r != "" {
		tokenName = "github-ci-" + strings.ReplaceAll(r, "/", "-")
	}
	t, err := createAPIToken(tokenName, "deploy", projectID)
	if err != nil {
		return fmt.Errorf("creating deploy token: %w", err)
	}
	fmt.Printf("\nCreated deploy token %q (limited to project %s).\n", t.Name, projectID)

	sets := []struct{ name, value string }{
		{"CAPSULE_TOKEN", t.Token}, {"CAPSULE_ORG_ID", orgID}, {"CAPSULE_PROJECT_ID", projectID},
	}
	for _, s := range sets {
		if err := runGH(append([]string{"secret", "set", s.name}, repoArgs...), s.value); err != nil {
			return fmt.Errorf("gh secret set %s: %w", s.name, err)
		}
	}
	apiURL := ""
	if cfg != nil {
		apiURL = cfg.APIURL
	}
	if v, _ := cmd.Flags().GetString("api-url"); v != "" {
		apiURL = v
	}
	if apiURL != "" {
		if err := runGH(append([]string{"variable", "set", "CAPSULE_API_URL", "--body", apiURL}, repoArgs...), ""); err != nil {
			return fmt.Errorf("gh variable set CAPSULE_API_URL: %w", err)
		}
	}
	fmt.Println("Queued/ran: secrets CAPSULE_TOKEN, CAPSULE_ORG_ID, CAPSULE_PROJECT_ID and variable CAPSULE_API_URL (gh output follows).")
	return nil
}

func init() {
	ciInitCmd.Flags().String("provider", "github", "CI provider (github)")
	ciInitCmd.Flags().String("stack", "auto", "Stack: auto, node, node-pnpm, node-yarn, node-bun, go, python, other")
	ciInitCmd.Flags().String("branch", "main", "Branch that deploys to production")
	ciInitCmd.Flags().String("working-directory", "", "App directory inside the repo (monorepo); default: repo root")
	ciInitCmd.Flags().String("action-ref", "main", "Ref of the Capsule deploy action to pin (e.g. main or a tag)")
	ciInitCmd.Flags().Bool("force", false, "Overwrite an existing workflow file")
	ciInitCmd.Flags().Bool("print", false, "Print the workflow to stdout instead of writing it")
	ciInitCmd.Flags().Bool("set-secrets", false, "Create a deploy token and set repo secrets/variables with gh")
	ciInitCmd.Flags().String("org", "", "Org ID (default: CAPSULE_ORG_ID or .capsule.json)")
	ciInitCmd.Flags().String("project", "", "Project ID (default: CAPSULE_PROJECT_ID or .capsule.json)")
	ciInitCmd.Flags().String("repo", "", "GitHub repository OWNER/NAME for gh (default: the current checkout)")
	ciCmd.AddCommand(ciInitCmd)
	rootCmd.AddCommand(ciCmd)
}
