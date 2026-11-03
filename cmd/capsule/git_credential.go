package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
	"github.com/kynto-consulting/capsule/cli/internal/config"
	"github.com/kynto-consulting/capsule/cli/internal/gitcred"
)

// ── capsule git credential get|store|erase ───────────────────────────────────
// Implements the git-credential helper protocol (gitcredentials(7)). Git runs
// `capsule git credential <action>` and talks key=value lines on stdin/stdout.
//
//   get    asks POST /user/git-credentials for a short-lived cgt_ token scoped
//          to the repository in `path` and prints username/password.
//   store  no-op: a token is never persisted.
//   erase  no-op: nothing is stored here (Git's own cache helper is told by Git).
//
// stdout carries only protocol lines. Diagnostics go to stderr and never
// contain the token.

// credCache is the in-process cache: a helper process serves one Git
// invocation, so it matters only to long-lived callers and tests.
var credCache = gitcred.NewCache()

// isHelperInvocation reports whether cmd is `capsule git credential ...`, which
// must never prompt or print anything but protocol lines.
func isHelperInvocation(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "credential" && c.Parent() != nil && c.Parent().Name() == "git" {
			return true
		}
	}
	return false
}

// cliSessionID is the id that binds Git credentials to this CLI login when the session is a JWT
// (a cap_ API token identifies the session by itself, so "" then). It is generated once, stored in
// the config file and cleared by `capsule logout`.
func cliSessionID() string {
	tok := os.Getenv("CAPSULE_TOKEN")
	if tok == "" && cfg != nil {
		tok = cfg.Token
	}
	if tok == "" || strings.HasPrefix(tok, "cap_") || cfg == nil {
		return ""
	}
	if cfg.CLISessionID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return ""
		}
		cfg.CLISessionID = hex.EncodeToString(b)
		_ = config.Save(cfg) // not persisted with CAPSULE_TOKEN: the id then lives for this process only
	}
	return cfg.CLISessionID
}

// issueCredential requests a token for host/path with the preferred scope and
// falls back from write to read when the role only allows reading.
func issueCredential(host, path string) (gitcred.Credential, error) {
	scopes := []string{"write", "read"}
	var lastErr error
	for _, scope := range scopes {
		var resp rec
		body := rec{"protocol": "https", "host": host, "path": path, "scope": scope}
		if sid := cliSessionID(); sid != "" {
			body["cli_session_id"] = sid // JWT login: ties the short-lived tokens to this CLI session
		}
		_, err := apiDo("POST", "/api/v1/user/git-credentials", body, &resp, nil)
		if err == nil {
			cred := gitcred.Credential{Username: sv(resp, "username"), Password: sv(resp, "password")}
			if t, e := time.Parse(time.RFC3339, sv(resp, "expires_at")); e == nil {
				cred.ExpiresAt = t
			}
			if t, e := time.Parse(time.RFC3339, sv(resp, "renew_after")); e == nil {
				cred.RenewAt = t
			}
			if cred.Username == "" || cred.Password == "" {
				return gitcred.Credential{}, codedErr(exitGit, "the server answered without a usable credential")
			}
			return cred, nil
		}
		lastErr = err
		if ae, ok := client.AsAPIError(err); ok && ae.Status == 403 && scope == "write" {
			continue // role allows read only: try read
		}
		break
	}
	return gitcred.Credential{}, lastErr
}

func credentialGet(in io.Reader, out io.Writer, errOut io.Writer) error {
	req, err := gitcred.ParseRequest(in)
	if err != nil {
		return usageErr("%v", err)
	}
	if req.Protocol != "https" {
		return nil // not ours (http, ssh, file, ...): a token never travels in clear text; answer nothing and let Git continue
	}
	tok, err := sessionToken()
	if err != nil {
		fmt.Fprintln(errOut, "capsule: "+firstLine(err.Error()))
		return silentErr(err)
	}
	_ = tok
	path := gitcred.NormalizePath(req.Path)
	if path == "" || !strings.Contains(path, "/") {
		return usageErr("Git sent no repository path: run `capsule git login` (it sets credential.useHttpPath=true so tokens are scoped to one repository)")
	}
	cred, _, err := credCache.Get(gitcred.Key(req.Host, path), func() (gitcred.Credential, error) {
		return issueCredential(req.Host, path)
	})
	if err != nil {
		if ae, ok := client.AsAPIError(err); ok {
			switch classifyAPI(ae) {
			case exitAuth:
				fmt.Fprintln(errOut, "capsule: session expired or invalid: run `capsule login`")
			case exitDenied:
				fmt.Fprintf(errOut, "capsule: no access to %s (the server hides repositories you cannot see)\n", path)
			default:
				fmt.Fprintf(errOut, "capsule: could not get a Git credential: %s %s\n", ae.Code, ae.Message)
			}
			return &exitError{code: classifyAPI(ae), msg: fmt.Sprintf("git credential: %s", ae.Message), err: err}
		}
		fmt.Fprintf(errOut, "capsule: could not get a Git credential: %v\n", err)
		return err
	}
	return cred.Write(out)
}

// silentErr keeps the exit code of err but prints nothing more: the helper has
// already written its diagnostic to stderr.
func silentErr(err error) error {
	return &exitError{code: exitCodeFor(err), msg: "", err: err}
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func init() {
	credGet := userLeaf("get", "git-credential get: print a short-lived token for the repository in stdin", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		return credentialGet(os.Stdin, os.Stdout, os.Stderr)
	})
	drain := func(cmd *cobra.Command, args []string) error {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return nil
	}
	credStore := userLeaf("store", "git-credential store: no-op (tokens are never persisted)", cobra.NoArgs, drain)
	credErase := userLeaf("erase", "git-credential erase: no-op (nothing is stored)", cobra.NoArgs, drain)
	cred := group("credential", "Git credential helper (run by Git, not by you)", credGet, credStore, credErase)
	cred.SilenceErrors = true
	// Never dump usage to stdout: Git reads it as protocol lines.
	cred.SilenceUsage = true
	cred.Long = "Git credential helper for Capsule Git. Configure it with `capsule git login`; Git then calls\n`capsule git credential get|store|erase` and speaks the git-credential protocol on stdin/stdout.\nTokens are short-lived (about 1 h), scoped to one repository, renewed automatically and never written to disk."
	gitCmd.AddCommand(cred)
}
