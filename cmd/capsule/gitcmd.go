package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// ── capsule git / previews ───────────────────────────────────────────────────

var gitCmd = &cobra.Command{
	Use:   "git",
	Short: "Link the project to a Git repository (GitHub, Forgejo, Gitea, generic)",
}

func gitPath(cmd *cobra.Command, suffix string) (string, error) {
	base, err := projBase(cmd)
	if err != nil {
		return "", err
	}
	return base + "/git" + suffix, nil
}

func printGitConfig(g rec) {
	row := func(k, v string) {
		if v != "" {
			fmt.Printf("  %-16s %s\n", k+":", v)
		}
	}
	row("provider", str(g, "provider"))
	row("repository", str(g, "repo_url"))
	row("branch", str(g, "branch"))
	row("auto deploy", str(g, "auto_deploy"))
	row("previews", str(g, "preview_enabled"))
	row("preview max", str(g, "preview_max"))
	row("preview ttl (h)", str(g, "preview_ttl_hours"))
	row("auth", str(g, "auth_kind"))
	row("webhook url", str(g, "webhook_url"))
	row("web url", str(g, "web_url"))
}

var gitLinkCmd = &cobra.Command{
	Use:   "link",
	Short: "Link or update the project's Git repository",
	Long: `Links the project to a repository. Credentials never go on the command line:
the access token is read from stdin with --token-stdin and an SSH private key
from a file with --ssh-key-file. Neither is ever printed or returned by the API.`,
	Example: `  capsule git link --provider github --repo-url https://github.com/acme/app --branch main
  printf %s "$GIT_TOKEN" | capsule git link --repo-url https://git.example.com/acme/app --provider forgejo --token-stdin --auto-deploy --previews`,
	RunE: func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		repo, _ := f.GetString("repo-url")
		if repo == "" {
			return fmt.Errorf("--repo-url is required")
		}
		provider, _ := f.GetString("provider")
		provider = strings.ToLower(provider)
		switch provider {
		case "github", "forgejo", "gitea", "generic":
		default:
			return fmt.Errorf("invalid --provider %q (github, forgejo, gitea, generic)", provider)
		}
		branch, _ := f.GetString("branch")
		body := rec{"provider": provider, "repo_url": repo, "branch": branch}
		for flag, key := range map[string]string{"auto-deploy": "auto_deploy", "previews": "preview_enabled"} {
			if f.Changed(flag) {
				v, _ := f.GetBool(flag)
				body[key] = v
			}
		}
		if f.Changed("preview-max") {
			v, _ := f.GetInt("preview-max")
			body["preview_max"] = v
		}
		if f.Changed("preview-ttl-hours") {
			v, _ := f.GetInt("preview-ttl-hours")
			body["preview_ttl_hours"] = v
		}
		if v, _ := f.GetBool("rotate-webhook-secret"); v {
			body["rotate_webhook_secret"] = true
		}
		auth, _ := f.GetString("auth")
		tokenStdin, _ := f.GetBool("token-stdin")
		keyFile, _ := f.GetString("ssh-key-file")
		if tokenStdin && keyFile != "" {
			return fmt.Errorf("use either --token-stdin or --ssh-key-file, not both")
		}
		if auth == "" {
			switch {
			case tokenStdin:
				auth = "token"
			case keyFile != "":
				auth = "ssh_key"
			default:
				auth = "none"
			}
		}
		auth = strings.ReplaceAll(strings.ToLower(auth), "-", "_")
		switch auth {
		case "token", "ssh_key", "none":
		default:
			return fmt.Errorf("invalid --auth %q (token, ssh-key, none)", auth)
		}
		if auth == "token" && !tokenStdin {
			return fmt.Errorf("--auth token needs the token on stdin: pass --token-stdin")
		}
		if tokenStdin && auth != "token" {
			return fmt.Errorf("--token-stdin requires --auth token")
		}
		if auth == "ssh_key" && keyFile == "" {
			return fmt.Errorf("--auth ssh-key needs --ssh-key-file <path>")
		}
		if auth == "none" && (tokenStdin || keyFile != "") {
			return fmt.Errorf("--auth none conflicts with --token-stdin/--ssh-key-file")
		}
		body["auth_kind"] = auth
		if tokenStdin {
			t, err := readStdinSecret("token")
			if err != nil {
				return err
			}
			body["token"] = t
		}
		if keyFile != "" {
			k, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("reading --ssh-key-file: %w", err)
			}
			body["ssh_key"] = string(k)
		}
		p, err := gitPath(cmd, "")
		if err != nil {
			return err
		}
		var resp rec
		if err := apiClient.Put(p, body, &resp); err != nil {
			return platErr(err, "project Git integration")
		}
		fmt.Println("Project linked to", repo)
		printGitConfig(resp)
		if s := str(resp, "webhook_secret"); s != "" {
			fmt.Printf("\nwebhook secret (shown ONCE; set it in the repository webhook):\n\n%s\n", s)
		}
		return nil
	},
}

func fetchGit(cmd *cobra.Command) (rec, error) {
	p, err := gitPath(cmd, "")
	if err != nil {
		return nil, err
	}
	var g rec
	if err := apiClient.Get(p, &g); err != nil {
		return nil, platErr(err, "project Git integration")
	}
	return g, nil
}

var gitShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the project's Git link (credentials are never shown)",
	RunE: func(cmd *cobra.Command, args []string) error {
		g, err := fetchGit(cmd)
		if err != nil {
			return err
		}
		delete(g, "token")
		delete(g, "ssh_key")
		if wantJSON(cmd) {
			return printJSON(g)
		}
		printGitConfig(g)
		return nil
	},
}

var gitRemoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Print the clone URL and the `git remote add capsule` command",
	RunE: func(cmd *cobra.Command, args []string) error {
		g, err := fetchGit(cmd)
		if err != nil {
			return err
		}
		ssh, _ := cmd.Flags().GetBool("ssh")
		u := str(g, "clone_url_https")
		if ssh {
			u = str(g, "clone_url_ssh")
		}
		if u == "" {
			return fmt.Errorf("the server returned no clone URL (is the project linked? try: capsule git repo)")
		}
		fmt.Println(u)
		fmt.Printf("git remote add capsule %s\n", u)
		return nil
	},
}

var gitUnlinkCmd = &cobra.Command{
	Use:   "unlink",
	Short: "Remove the Git link (the repository itself is not touched)",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := gitPath(cmd, "")
		if err != nil {
			return err
		}
		if err := abortUnless(cmd, "Unlink the project from its Git repository? Auto deploys and previews stop."); err != nil {
			return err
		}
		if err := apiClient.Delete(p); err != nil {
			return platErr(err, "project Git integration")
		}
		fmt.Println("Git link removed.")
		return nil
	},
}

var gitTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Check that Capsule can reach the repository with the saved credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := gitPath(cmd, "/test")
		if err != nil {
			return err
		}
		var r rec
		if err := apiClient.Post(p, rec{}, &r); err != nil {
			return platErr(err, "project Git integration")
		}
		if wantJSON(cmd) {
			return printJSON(r)
		}
		ok := str(r, "ok", "success")
		msg := str(r, "message", "error")
		if ok == "false" {
			return fmt.Errorf("git test failed: %s", msg)
		}
		fmt.Println("Git test OK", msg)
		return nil
	},
}

var gitRepoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Create the repository in Capsule's own Git (optionally importing another URL)",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := gitPath(cmd, "/repo")
		if err != nil {
			return err
		}
		body := rec{}
		if u, _ := cmd.Flags().GetString("import-url"); u != "" {
			body["import_url"] = u
		}
		var r rec
		if err := apiClient.Post(p, body, &r); err != nil {
			return platErr(err, "the platform Git service")
		}
		fmt.Println("Repository created.")
		printGitConfig(r)
		if u := str(r, "clone_url_https"); u != "" {
			fmt.Printf("  clone:           %s\n", u)
		}
		return nil
	},
}

var gitBranchesCmd = &cobra.Command{
	Use:   "branches",
	Short: "List branches of the linked repository",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := gitPath(cmd, "/branches")
		if err != nil {
			return err
		}
		v, err := getRaw(p)
		if err != nil {
			return platErr(err, "project Git integration")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		w := newTW()
		fmt.Fprintln(w, "BRANCH\tSHA\tDEFAULT")
		for _, b := range asList(v, "branches") {
			fmt.Fprintf(w, "%s\t%s\t%s\n", str(b, "name"), shortSHA(str(b, "sha", "commit")), str(b, "default"))
		}
		return w.Flush()
	},
}

var gitCommitsCmd = &cobra.Command{
	Use:   "commits",
	Short: "List commits of a branch",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := gitPath(cmd, "/commits")
		if err != nil {
			return err
		}
		branch, _ := cmd.Flags().GetString("branch")
		page, _ := cmd.Flags().GetInt("page")
		q := fmt.Sprintf("?page=%d", page)
		if branch != "" {
			q = "?branch=" + urlEscape(branch) + "&page=" + fmt.Sprint(page)
		}
		v, err := getRaw(p + q)
		if err != nil {
			return platErr(err, "project Git integration")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		w := newTW()
		fmt.Fprintln(w, "SHA\tAUTHOR\tDATE\tMESSAGE")
		for _, c := range asList(v, "commits") {
			msg := strings.SplitN(str(c, "message"), "\n", 2)[0]
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", shortSHA(str(c, "sha", "id")), sv(c, "author.login", "author"), str(c, "date", "created_at"), truncate(msg, 70))
		}
		return w.Flush()
	},
}

var gitPullsCmd = &cobra.Command{
	Use:   "pulls",
	Short: "List pull requests",
	RunE: func(cmd *cobra.Command, args []string) error {
		state, _ := cmd.Flags().GetString("state")
		switch state {
		case "open", "closed", "all":
		default:
			return fmt.Errorf("invalid --state %q (open, closed, all)", state)
		}
		p, err := gitPath(cmd, "/pulls?state="+state)
		if err != nil {
			return err
		}
		v, err := getRaw(p)
		if err != nil {
			return platErr(err, "project Git integration")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		w := newTW()
		fmt.Fprintln(w, "PR\tSTATE\tAUTHOR\tHEAD\tTITLE")
		for _, pr := range asList(v, "pulls") {
			fmt.Fprintf(w, "#%s\t%s\t%s\t%s\t%s\n", str(pr, "number", "id"), str(pr, "state"), sv(pr, "author.login", "author"), str(pr, "head", "head_ref"), truncate(str(pr, "title"), 60))
		}
		return w.Flush()
	},
}

func urlEscape(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", "&", "%26", "#", "%23", "?", "%3F", "+", "%2B")
	return r.Replace(s)
}

// ── previews ─────────────────────────────────────────────────────────────────

var previewsCmd = &cobra.Command{
	Use:   "previews",
	Short: "Per-pull-request preview environments",
}

var previewsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List preview environments",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		v, err := getRaw(base + "/previews")
		if err != nil {
			return platErr(err, "previews")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		w := newTW()
		fmt.Fprintln(w, "PR\tSTATUS\tREF\tSHA\tURL\tEXPIRES")
		for _, p := range asList(v, "previews") {
			fmt.Fprintf(w, "#%s\t%s\t%s\t%s\t%s\t%s\n", str(p, "pr", "pr_number"), str(p, "status"), str(p, "ref"), shortSHA(str(p, "sha")), str(p, "url"), str(p, "expires_at"))
		}
		return w.Flush()
	},
}

var previewsCreateCmd = &cobra.Command{
	Use:     "create --pr N",
	Short:   "Create or redeploy the preview of a pull request",
	Example: `  capsule previews create --pr 42 --ref feature/x --sha $(git rev-parse HEAD)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		pr, _ := f.GetInt("pr")
		if pr <= 0 {
			return fmt.Errorf("--pr is required (pull request number)")
		}
		ref, _ := f.GetString("ref")
		sha, _ := f.GetString("sha")
		if ref == "" {
			ref = firstEnv("GITHUB_HEAD_REF", "GITHUB_REF_NAME")
		}
		if sha == "" {
			sha = os.Getenv("GITHUB_SHA")
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		var r rec
		if err := apiClient.Post(base+"/previews", rec{"pr": pr, "ref": ref, "sha": sha}, &r); err != nil {
			return platErr(err, "previews")
		}
		fmt.Printf("Preview for PR #%d requested.\n", pr)
		if u := str(r, "url"); u != "" {
			fmt.Println("  url:", u)
		}
		return nil
	},
}

var previewsDeleteCmd = &cobra.Command{
	Use:   "delete PR",
	Short: "Delete the preview of a pull request",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		n := strings.TrimPrefix(args[0], "#")
		for _, c := range n {
			if c < '0' || c > '9' {
				return fmt.Errorf("PR must be a number, got %q", args[0])
			}
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		if err := abortUnless(cmd, "Delete the preview of PR #"+n+"?"); err != nil {
			return err
		}
		if err := apiClient.Delete(base + "/previews/" + n); err != nil {
			return platErr(err, "previews")
		}
		fmt.Printf("Preview of PR #%s deleted.\n", n)
		return nil
	},
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func init() {
	for _, c := range []*cobra.Command{gitLinkCmd, gitShowCmd, gitRemoteCmd, gitUnlinkCmd, gitTestCmd, gitRepoCmd, gitBranchesCmd, gitCommitsCmd, gitPullsCmd,
		previewsListCmd, previewsCreateCmd, previewsDeleteCmd} {
		addNetFlags(c)
	}
	l := gitLinkCmd.Flags()
	l.String("provider", "github", "github, forgejo, gitea or generic")
	l.String("repo-url", "", "Repository URL")
	l.String("branch", "main", "Production branch")
	l.Bool("auto-deploy", true, "Deploy on push to the branch")
	l.Bool("previews", false, "Enable pull request previews")
	l.Int("preview-max", 0, "Maximum simultaneous previews")
	l.Int("preview-ttl-hours", 0, "Hours before an idle preview is removed")
	l.String("auth", "", "token, ssh-key or none (default: inferred from the flags below)")
	l.Bool("token-stdin", false, "Read the access token from stdin (never echoed)")
	l.String("ssh-key-file", "", "Path to an SSH private key to send (never echoed)")
	l.Bool("rotate-webhook-secret", false, "Generate a new webhook secret")
	gitRemoteCmd.Flags().Bool("ssh", false, "Print the SSH clone URL instead of HTTPS")
	gitUnlinkCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	gitRepoCmd.Flags().String("import-url", "", "Import the content from this URL")
	gitCommitsCmd.Flags().String("branch", "", "Branch (default: the repository default)")
	gitCommitsCmd.Flags().Int("page", 1, "Page number")
	gitPullsCmd.Flags().String("state", "open", "open, closed or all")
	gitCmd.AddCommand(gitLinkCmd, gitShowCmd, gitUnlinkCmd, gitTestCmd, gitRemoteCmd, gitRepoCmd, gitBranchesCmd, gitCommitsCmd, gitPullsCmd)
	rootCmd.AddCommand(gitCmd)

	previewsCreateCmd.Flags().Int("pr", 0, "Pull request number")
	previewsCreateCmd.Flags().String("ref", "", "Head branch (default: GITHUB_HEAD_REF)")
	previewsCreateCmd.Flags().String("sha", "", "Head commit SHA (default: GITHUB_SHA)")
	previewsDeleteCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	previewsCmd.AddCommand(previewsListCmd, previewsCreateCmd, previewsDeleteCmd)
	rootCmd.AddCommand(previewsCmd)
}
