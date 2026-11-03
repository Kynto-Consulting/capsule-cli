package main

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ── capsule git clone | login | setup-gcm ────────────────────────────────────
// SPEC 6.5 / CLI.md 3.1.1. The credential helper itself is in git_credential.go.

// helperCmd is what Git runs as a credential helper. The leading "!" is
// required: without it Git would look for an executable named
// "git-credential-capsule git credential".
const defaultHelperCmd = "!capsule git credential"

// ephemeralCacheHelper keeps the token in the memory of Git's credential-cache
// daemon for 50 minutes: shorter than the 1 h token minus the 10-minute
// renewal margin, and never on disk. Git for Windows has no such daemon.
const ephemeralCacheHelper = "cache --timeout=3000"

// gitCloneCmd is `capsule git clone`, shared with the `capsule repo clone` alias.
var gitCloneCmd *cobra.Command

var repoRefRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// sessionToken returns the CLI session token or an exit-coded error. A service
// token (cgt_) is refused: CI must use the per-run or service token directly.
func sessionToken() (string, error) {
	tok := os.Getenv("CAPSULE_TOKEN")
	if tok == "" && cfg != nil {
		tok = cfg.Token
	}
	if strings.HasPrefix(tok, "cgt_") {
		return "", usageErr("CAPSULE_TOKEN is a Git service token (cgt_...): the credential helper uses the signed-in CLI session. In CI use the per-run token or the service token as the Git password")
	}
	if tok == "" {
		return "", codedErr(exitAuth, "not signed in: run `capsule login`")
	}
	return tok, nil
}

func hostIsWindows() bool {
	return runtime.GOOS == "windows" || (runtime.GOOS == "js" && os.Getenv("HOMEDRIVE") != "")
}

// gitHosts resolves the Git base URL (https://git.example.com) and SSH host.
func gitHosts(cmd *cobra.Command) (httpsURL, sshHost string, err error) {
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		httpsURL = h
	} else if h := os.Getenv("CAPSULE_GIT_URL"); h != "" {
		httpsURL = h
	}
	var caps rec
	if httpsURL == "" || cmd.Flags().Lookup("ssh") != nil {
		if _, e := apiDo("GET", "/api/v1/platform/capabilities", nil, &caps, nil); e != nil && httpsURL == "" {
			return "", "", e
		}
		g, _ := caps["git"].(map[string]any)
		if httpsURL == "" {
			if on, _ := g["enabled"].(bool); !on && g["url"] == nil {
				return "", "", codedErr(exitGit, "Git Capsule is not enabled on this Capsule server (see `capsule platform`)")
			}
			httpsURL = sv(g, "url")
		}
		sshHost = sv(g, "ssh_host")
	}
	if !strings.HasPrefix(httpsURL, "https://") && !strings.HasPrefix(httpsURL, "http://") {
		if httpsURL == "" {
			return "", "", codedErr(exitGit, "the server did not report its Git URL: pass --host https://git.example.com")
		}
		httpsURL = "https://" + httpsURL
	}
	return strings.TrimRight(httpsURL, "/"), sshHost, nil
}

// cfgKV is one git config key/value. multi means the key holds a list.
type cfgKV struct {
	key, val string
	multi    bool
}

// helperConfig lists the git config keys that point Git at the helper for one
// host. Written with helperSteps they are idempotent: the key is rewritten,
// never appended twice.
func helperConfig(hostURL, helper string, noCache bool) []cfgKV {
	key := "credential." + hostURL + ".helper"
	// The empty value resets helpers inherited from broader scopes (for
	// example a system-wide Git Credential Manager) for this host only.
	kvs := []cfgKV{{key, "", true}}
	if !noCache && !hostIsWindows() {
		kvs = append(kvs, cfgKV{key, ephemeralCacheHelper, true})
	}
	kvs = append(kvs, cfgKV{key, helper, true}, cfgKV{"credential." + hostURL + ".useHttpPath", "true", false})
	return kvs
}

func helperSteps(hostURL, helper string, local, noCache bool) []gitStep {
	scope := "--global"
	if local {
		scope = "--local"
	}
	var steps []gitStep
	seen := map[string]bool{}
	for _, kv := range helperConfig(hostURL, helper, noCache) {
		switch {
		case kv.multi && !seen[kv.key]:
			seen[kv.key] = true
			steps = append(steps, gitStep{Args: []string{"config", scope, "--replace-all", kv.key, kv.val}})
		case kv.multi:
			steps = append(steps, gitStep{Args: []string{"config", scope, "--add", kv.key, kv.val}})
		default:
			steps = append(steps, gitStep{Args: []string{"config", scope, kv.key, kv.val}})
		}
	}
	return steps
}

func addHelperFlags(c *cobra.Command) {
	c.Flags().String("host", "", "Git base URL (default: the server's Git URL from `capsule platform`)")
	c.Flags().String("helper", defaultHelperCmd, "Credential helper command written to git config")
	c.Flags().Bool("no-cache", false, "Do not add Git's in-memory credential cache in front of the helper")
}

func addSessionLine() error {
	var me rec
	if _, err := apiDo("GET", "/api/v1/auth/me", nil, &me, nil); err != nil {
		return err
	}
	stderrf("✓ Active session (%s)\n", sv(me, "email", "name"))
	return nil
}

func init() {
	clone := userLeaf("clone ORG/REPO [DIR] [-- GIT-ARGS...]", "Clone a repository with the active CLI session (short-lived token, no secret in git config)", cobra.MinimumNArgs(1), func(cmd *cobra.Command, args []string) error {
		if _, err := sessionToken(); err != nil {
			return err
		}
		dash := cmd.ArgsLenAtDash()
		pos, extra := args, []string(nil)
		if dash >= 0 {
			pos, extra = args[:dash], args[dash:]
		}
		if len(pos) == 0 || len(pos) > 2 {
			return usageErr("usage: capsule git clone ORG/REPO [DIR] [-- git-args]")
		}
		repo := strings.TrimSuffix(strings.TrimPrefix(pos[0], "/"), ".git")
		if !repoRefRe.MatchString(repo) {
			return usageErr("repository must be ORG/REPO, got %q", pos[0])
		}
		if err := addSessionLine(); err != nil {
			return err
		}
		f := cmd.Flags()
		ssh, _ := f.GetBool("ssh")
		local, _ := f.GetBool("local")
		noCache, _ := f.GetBool("no-cache")
		helper, _ := f.GetString("helper")
		hostURL, sshHost, err := gitHosts(cmd)
		if err != nil {
			return err
		}
		var steps []gitStep
		cloneArgs := []string{"clone"}
		if ssh {
			if sshHost == "" {
				return codedErr(exitGit, "the server reported no SSH host; use HTTPS or pass --host")
			}
			cloneArgs = append(cloneArgs, extra...)
			cloneArgs = append(cloneArgs, "git@"+sshHost+":"+repo+".git")
		} else {
			if local {
				// The helper cannot be written into a repo that does not exist
				// yet: `clone -c` stores these keys in the new repo's config.
				for _, kv := range helperConfig(hostURL, helper, noCache) {
					cloneArgs = append(cloneArgs, "-c", kv.key+"="+kv.val)
				}
				stderrf("✓ Credential helper configured in the cloned repository only (%s)\n", hostURL)
			} else {
				steps = append(steps, helperSteps(hostURL, helper, false, noCache)...)
				stderrf("✓ Credential helper configured for %s (scope per repository)\n", hostURL)
			}
			cloneArgs = append(cloneArgs, extra...)
			cloneArgs = append(cloneArgs, hostURL+"/"+repo+".git")
		}
		if len(pos) == 2 {
			cloneArgs = append(cloneArgs, pos[1])
		}
		steps = append(steps, gitStep{Args: cloneArgs})
		return runGitSteps(steps)
	})
	clone.Flags().Bool("ssh", false, "Clone over SSH instead of HTTPS")
	clone.Flags().Bool("local", false, "Configure the helper only in the cloned repository, not globally")
	addHelperFlags(clone)

	login := userLeaf("login", "Check the CLI session and configure the Git credential helper (idempotent)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		if _, err := sessionToken(); err != nil {
			return err
		}
		f := cmd.Flags()
		if st, _ := f.GetBool("status"); st {
			return gitLoginStatus(cmd)
		}
		if err := addSessionLine(); err != nil {
			return err
		}
		local, _ := f.GetBool("local")
		noCache, _ := f.GetBool("no-cache")
		helper, _ := f.GetString("helper")
		hostURL, _, err := gitHosts(cmd)
		if err != nil {
			return err
		}
		if err := runGitSteps(helperSteps(hostURL, helper, local, noCache)); err != nil {
			return err
		}
		fmt.Printf("✓ Credential helper configured for %s (scope per repository, tokens expire in about 1 h and renew automatically)\n", hostURL)
		return nil
	})
	login.Flags().Bool("status", false, "List the repositories with a valid short-lived token instead")
	login.Flags().Bool("local", false, "Configure the current repository only")
	addHelperFlags(login)

	gcm := userLeaf("setup-gcm", "Write the Git Credential Manager OAuth keys for the Git host (alternative to the CLI helper)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		hostURL, _, err := gitHosts(cmd)
		if err != nil {
			return err
		}
		clientID, _ := f.GetString("client-id")
		if clientID == "" {
			clientID = os.Getenv("CAPSULE_GCM_CLIENT_ID")
		}
		if clientID == "" {
			// Linked project: the repository's clone options carry the exact keys.
			if p, perr := gp(cmd, "/repo"); perr == nil {
				var repo rec
				if _, e := apiDo("GET", p, nil, &repo, nil); e == nil {
					return applyGCMKeys(cmd, hostURL, repo)
				}
			}
			return usageErr("the OAuth client id is not known here. Pass --client-id ID (copy it from the Clone dialog of a repository, or from the Forgejo OAuth2 provider documentation), or run this inside a linked project so the server can provide it")
		}
		keys := [][2]string{
			{"oauthClientId", clientID},
			{"oauthAuthorizeEndpoint", "/login/oauth/authorize"},
			{"oauthTokenEndpoint", "/login/oauth/access_token"},
			{"oauthRedirectUri", "http://127.0.0.1"},
		}
		return writeGCM(cmd, hostURL, keys)
	})
	gcm.Flags().String("host", "", "Git base URL (default: the server's Git URL)")
	gcm.Flags().String("client-id", "", "Pre-registered OAuth client id of Git Credential Manager on the Forgejo server")
	gcm.Flags().Bool("local", false, "Write to the current repository's config only")
	// the project flags are optional here: only used to read the keys from the repo
	gcm.Flags().String("org", "", "Org ID (to read the keys from a repository)")
	gcm.Flags().String("project", "", "Project ID (to read the keys from a repository)")

	gitCloneCmd = clone
	gitCmd.AddCommand(clone, login, gcm)
}

func applyGCMKeys(cmd *cobra.Command, hostURL string, repo rec) error {
	clone, _ := repo["clone"].(map[string]any)
	https, _ := clone["https"].(map[string]any)
	list, _ := https["gcm_config"].([]any)
	var keys [][2]string
	prefix := "credential." + hostURL + "."
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		k := sv(m, "key")
		v := sv(m, "value")
		k = strings.TrimPrefix(k, prefix)
		if i := strings.LastIndex(k, "."); i >= 0 && strings.Contains(k, "credential.") {
			k = k[i+1:]
		}
		if k != "" && v != "" {
			keys = append(keys, [2]string{k, v})
		}
	}
	if len(keys) == 0 {
		return usageErr("the server returned no Git Credential Manager keys for this repository; pass --client-id ID")
	}
	return writeGCM(cmd, hostURL, keys)
}

func writeGCM(cmd *cobra.Command, hostURL string, keys [][2]string) error {
	scope := "--global"
	if local, _ := cmd.Flags().GetBool("local"); local {
		scope = "--local"
	}
	var steps []gitStep
	for _, kv := range keys {
		if !regexp.MustCompile(`^oauth[A-Za-z]+$`).MatchString(kv[0]) {
			return usageErr("refusing to write unexpected key %q", kv[0])
		}
		steps = append(steps, gitStep{Args: []string{"config", scope, "credential." + hostURL + "." + kv[0], kv[1]}})
	}
	if err := runGitSteps(steps); err != nil {
		return err
	}
	fmt.Printf("✓ Git Credential Manager keys written for %s\n", hostURL)
	fmt.Println("  Note: this route gives the token the rights of your Forgejo user (no per-repository scope) and is not listed under Access tokens.")
	fmt.Println("  Do not use it with platform administrator accounts: use `capsule git clone` or SSH.")
	return nil
}

func gitLoginStatus(cmd *cobra.Command) error {
	var raw any
	if _, err := apiDo("GET", "/api/v1/user/git-tokens", nil, &raw, nil); err != nil {
		return err
	}
	items, _ := pageOf(raw)
	now := time.Now()
	var live []rec
	for _, t := range items {
		if sv(t, "kind") != "ephemeral" || sv(t, "revoked_at") != "" {
			continue
		}
		if exp, err := time.Parse(time.RFC3339, sv(t, "expires_at")); err == nil && exp.Before(now) {
			continue
		}
		live = append(live, t)
	}
	if done, err := emit(cmd, live); done || err != nil {
		return err
	}
	t := newTable(cmd, "ID", "SCOPE", "REPOSITORIES", "EXPIRES", "LAST USED")
	for _, tk := range live {
		t.row(short8(sv(tk, "id")), sv(tk, "scope"), sv(tk, "projects"), sv(tk, "expires_at"), age(sv(tk, "last_used_at")))
	}
	if err := t.flush(); err != nil {
		return err
	}
	return nil
}
