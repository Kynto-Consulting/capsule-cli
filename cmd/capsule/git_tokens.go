package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// ── capsule git tokens | ssh-keys | service-tokens, capsule logout ───────────
// operationIds: gitTokensList gitTokenCreate gitTokenRevoke gitSshKeysList
// gitSshKeyAdd gitSshKeyDelete serviceTokensList serviceTokenCreate
// serviceTokenRevoke. A token value is printed once, to stdout, and never
// stored by the CLI.

func expiryDays(cmd *cobra.Command) (int, error) {
	s, _ := cmd.Flags().GetString("expires")
	d, err := parseDays(s)
	if err != nil {
		return 0, err
	}
	days := int((d + 24*time.Hour - 1) / (24 * time.Hour))
	if days < 1 || days > 90 {
		return 0, usageErr("--expires must be between 1d and 90d, got %q", s)
	}
	return days, nil
}

func scopeFlag(cmd *cobra.Command) (string, error) {
	s, _ := cmd.Flags().GetString("scope")
	if s != "read" && s != "write" {
		return "", usageErr("--scope must be read or write, got %q", s)
	}
	return s, nil
}

func tokenRows(cmd *cobra.Command, items []rec) error {
	t := newTable(cmd, "ID", "NAME", "KIND", "SCOPE", "REPOS", "EXPIRES", "LAST USED", "STATE")
	for _, tk := range items {
		state := "active"
		if sv(tk, "revoked_at") != "" {
			state = "revoked"
		}
		repos := sv(tk, "projects")
		if repos == "" {
			repos = "all"
		}
		t.row(short8(sv(tk, "id")), sv(tk, "name"), sv(tk, "kind"), sv(tk, "scope"), truncate(repos, 24), sv(tk, "expires_at"), age(sv(tk, "last_used_at")), state)
	}
	return t.flush()
}

// printNewToken shows a freshly created token once. The value is the only
// thing on stdout in table mode so it can be captured with $(...).
func printNewToken(cmd *cobra.Command, out rec, what string) error {
	if done, err := emit(cmd, out); done || err != nil {
		return err
	}
	tok := sv(out, "token")
	if tok == "" {
		return codedErr(exitGit, "the server did not return the token value")
	}
	stderrf("%s %q created (id %s, expires %s). The value is shown ONCE; store it now:\n", what, sv(out, "name"), short8(sv(out, "id")), sv(out, "expires_at"))
	fmt.Println(tok)
	return nil
}

func init() {
	// ── tokens ───────────────────────────────────────────────────────────────
	tlist := userLeaf("list", "List your Git access tokens (values are never shown)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		items, raw, err := fetchList(cmd, "/api/v1/user/git-tokens")
		if err != nil {
			return err
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		if quietIDs(cmd, items) {
			return nil
		}
		return tokenRows(cmd, items)
	})
	addList(tlist)
	tcreate := orgLeaf("create", "Create a personal Git access token (value shown once)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return usageErr("--name is required")
		}
		scope, err := scopeFlag(cmd)
		if err != nil {
			return err
		}
		days, err := expiryDays(cmd)
		if err != nil {
			return err
		}
		body := rec{"name": name, "scope": scope, "expires_in_days": days}
		repos := csvFlag(cmd, "repo")
		if len(repos) > 0 {
			org, err := resolveOrg(cmd)
			if err != nil {
				return err
			}
			var ids []string
			for _, r := range repos {
				id := r
				if len(r) < 36 {
					name := r
					if i := strings.LastIndex(r, "/"); i >= 0 {
						name = r[i+1:]
					}
					if id, err = resolveProjectID(org, name); err != nil {
						return usageErr("--repo %q: %v", r, err)
					}
				}
				ids = append(ids, id)
			}
			body["projects"] = ids
		}
		var out rec
		if _, err := apiDo("POST", "/api/v1/user/git-tokens", body, &out, nil); err != nil {
			return err
		}
		return printNewToken(cmd, out, "Token")
	})
	tcreate.Flags().String("name", "", "Token name")
	tcreate.Flags().String("scope", "read", "read or write")
	tcreate.Flags().StringSlice("repo", nil, "Restrict to these repositories/projects (name, ORG/NAME or id; default: all you can access)")
	tcreate.Flags().String("expires", "30d", "Lifetime: 1d to 90d")
	trevoke := userLeaf("revoke ID", "Revoke a Git access token", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := resolveShort("/api/v1/user/git-tokens", args[0], "token")
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Revoke token "+short8(id)+"?"); err != nil {
			return err
		}
		if _, err := apiDo("DELETE", "/api/v1/user/git-tokens/"+seg(id), nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("Token %s revoked.\n", short8(id))
		return nil
	})
	addYes(trevoke)
	gitCmd.AddCommand(group("tokens", "Personal Git access tokens", tlist, tcreate, trevoke))

	// ── ssh-keys ─────────────────────────────────────────────────────────────
	klist := userLeaf("list", "List your SSH keys", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		var raw any
		if _, err := apiDo("GET", "/api/v1/user/git-ssh-keys", nil, &raw, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		items, _ := pageOf(raw)
		if quietIDs(cmd, items) {
			return nil
		}
		t := newTable(cmd, "ID", "TITLE", "FINGERPRINT", "LAST USED", "SYNCED")
		for _, k := range items {
			t.row(short8(sv(k, "id")), sv(k, "title"), sv(k, "fingerprint"), age(sv(k, "last_used_at")), yn(k["forgejo_synced"] == true))
		}
		return t.flush()
	})
	klist.Flags().BoolP("quiet", "q", false, "Print only IDs")
	kadd := userLeaf("add", "Add a public SSH key", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		title, _ := cmd.Flags().GetString("title")
		if title == "" {
			return usageErr("--title is required")
		}
		file, _ := cmd.Flags().GetString("key-file")
		literal, _ := cmd.Flags().GetString("key")
		if file != "" && literal != "" {
			return usageErr("use either --key-file or --key")
		}
		key := strings.TrimSpace(literal)
		if key == "" {
			if file == "" {
				home, _ := os.UserHomeDir()
				file = filepath.Join(home, ".ssh", "id_ed25519.pub")
			}
			b, err := os.ReadFile(file)
			if err != nil {
				return usageErr("reading the public key: %v (pass --key-file PATH)", err)
			}
			key = strings.TrimSpace(string(b))
		}
		if strings.Contains(key, "PRIVATE KEY") {
			return usageErr("that is a PRIVATE key: never send it. Use the .pub file")
		}
		if !strings.HasPrefix(key, "ssh-") && !strings.HasPrefix(key, "ecdsa-") && !strings.HasPrefix(key, "sk-") {
			return usageErr("not an SSH public key (expected it to start with ssh-ed25519, ssh-rsa, ecdsa-...)")
		}
		var out rec
		if _, err := apiDo("POST", "/api/v1/user/git-ssh-keys", rec{"title": title, "key": key}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("SSH key %q added (%s).\n", title, orDash(sv(out, "fingerprint")))
		return nil
	})
	kadd.Flags().String("title", "", "Key title")
	kadd.Flags().String("key-file", "", "Public key file (default ~/.ssh/id_ed25519.pub)")
	kadd.Flags().String("key", "", "Public key text")
	krm := userLeaf("remove ID", "Remove an SSH key", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := resolveShort("/api/v1/user/git-ssh-keys", args[0], "SSH key")
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Remove SSH key "+short8(id)+"?"); err != nil {
			return err
		}
		if _, err := apiDo("DELETE", "/api/v1/user/git-ssh-keys/"+seg(id), nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("SSH key %s removed.\n", short8(id))
		return nil
	})
	addYes(krm)
	gitCmd.AddCommand(group("ssh-keys", "SSH keys used for git@ access", klist, kadd, krm))

	// ── service-tokens ───────────────────────────────────────────────────────
	slist := leaf("list", "List the Git service tokens of the project (CI)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/service-tokens")
		if err != nil {
			return err
		}
		var raw any
		if _, err := apiDo("GET", p, nil, &raw, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		items, _ := pageOf(raw)
		if quietIDs(cmd, items) {
			return nil
		}
		return tokenRows(cmd, items)
	})
	slist.Flags().BoolP("quiet", "q", false, "Print only IDs")
	screate := leaf("create", "Create a service token for CI: store the value as a CI secret", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return usageErr("--name is required")
		}
		scope, err := scopeFlag(cmd)
		if err != nil {
			return err
		}
		days, err := expiryDays(cmd)
		if err != nil {
			return err
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		pid := base[strings.LastIndex(base, "/")+1:]
		var out rec
		if _, err := apiDo("POST", base+"/git/service-tokens", rec{"name": name, "scope": scope, "expires_in_days": days, "projects": []string{pid}}, &out, nil); err != nil {
			return err
		}
		return printNewToken(cmd, out, "Service token")
	})
	screate.Flags().String("name", "", "Token name")
	screate.Flags().String("scope", "read", "read or write")
	screate.Flags().String("expires", "90d", "Lifetime: 1d to 90d")
	srev := leaf("revoke ID", "Revoke a service token", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		lp, err := gp(cmd, "/service-tokens")
		if err != nil {
			return err
		}
		id, err := resolveShort(lp, args[0], "service token")
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Revoke service token "+short8(id)+"? CI jobs using it will fail."); err != nil {
			return err
		}
		if _, err := apiDo("DELETE", lp+"/"+seg(id), nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("Service token %s revoked.\n", short8(id))
		return nil
	})
	addYes(srev)
	gitCmd.AddCommand(group("service-tokens", "Git service tokens for CI (not tied to a person)", slist, screate, srev))

	// ── logout ───────────────────────────────────────────────────────────────
	logout := userLeaf("logout", "Revoke this session's short-lived Git tokens and forget the local login", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		revoked := 0
		if cfg != nil && cfg.Token != "" && !config.TokenFromEnv() {
			var out rec
			path := "/api/v1/user/git-tokens"
			if sid := cliSessionID(); sid != "" {
				path += "?session_id=" + url.QueryEscape(sid)
			}
			_, derr := apiDo("DELETE", path, nil, &out, nil)
			if derr == nil {
				revoked, _ = strconv.Atoi(sv(out, "revoked"))
			}
			var raw any
			if derr != nil { // a server without the session endpoint: revoke the listed ephemeral tokens one by one
				if _, err := apiDo("GET", "/api/v1/user/git-tokens", nil, &raw, nil); err != nil {
					raw = nil
				}
			}
			if raw != nil {
				items, _ := pageOf(raw)
				for _, tk := range items {
					if sv(tk, "kind") != "ephemeral" || sv(tk, "revoked_at") != "" {
						continue
					}
					if _, err := apiDo("DELETE", "/api/v1/user/git-tokens/"+seg(sv(tk, "id")), nil, nil, nil); err == nil {
						revoked++
					}
				}
			}
			cfg.Token, cfg.RefreshToken, cfg.CLISessionID = "", "", ""
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
		}
		fmt.Printf("Logged out. %s short-lived Git token(s) revoked.\n", strconv.Itoa(revoked))
		return nil
	})
	rootCmd.AddCommand(logout)
}
