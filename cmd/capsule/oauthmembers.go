package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ── capsule oauth clients ────────────────────────────────────────────────────

const oauthPath = "/api/v1/oauth/clients"

var oauthCmd = &cobra.Command{
	Use:   "oauth",
	Short: "OAuth / OIDC clients of the platform",
}

var oauthClientsCmd = &cobra.Command{
	Use:   "clients",
	Short: "Manage OAuth clients (apps that sign in with Capsule)",
}

func oauthClientList() ([]rec, error) {
	v, err := getRaw(oauthPath)
	if err != nil {
		return nil, platErr(err, "OAuth clients")
	}
	return asList(v, "clients"), nil
}

func oauthResolve(ref string) (string, error) {
	list, err := oauthClientList()
	if err != nil {
		return "", err
	}
	for _, r := range list {
		if str(r, "client_id") == ref {
			return str(r, "id", "client_id"), nil
		}
	}
	r, err := pickByPrefix(list, ref, "OAuth client", "id")
	if err != nil {
		return "", err
	}
	return str(r, "id"), nil
}

func redirectURIs(cmd *cobra.Command) ([]string, error) {
	uris, _ := cmd.Flags().GetStringSlice("redirect-uri")
	for _, u := range uris {
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://localhost") && !strings.HasPrefix(u, "http://127.0.0.1") {
			return nil, fmt.Errorf("redirect URI %q must be https:// (http only for localhost)", u)
		}
	}
	return uris, nil
}

var oauthCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an OAuth client (the secret is shown once)",
	Example: `  capsule oauth clients create --name "My app" --redirect-uri https://app.example.com/callback
  capsule oauth clients create --name "SPA" --redirect-uri http://localhost:3000/cb --public`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		uris, err := redirectURIs(cmd)
		if err != nil {
			return err
		}
		if len(uris) == 0 {
			return fmt.Errorf("at least one --redirect-uri is required")
		}
		body := rec{"name": name, "redirect_uris": uris}
		if o, _ := cmd.Flags().GetString("org-id"); o != "" {
			body["org_id"] = o
		}
		if b, _ := cmd.Flags().GetBool("first-party"); b {
			body["first_party"] = true
		}
		if b, _ := cmd.Flags().GetBool("public"); b {
			body["public"] = true
		}
		var resp rec
		if err := apiClient.Post(oauthPath, body, &resp); err != nil {
			return platErr(err, "OAuth clients")
		}
		secret := str(resp, "client_secret")
		delete(resp, "client_secret")
		if wantJSON(cmd) {
			if secret != "" {
				resp["client_secret"] = secret
			}
			return printJSON(resp)
		}
		fmt.Printf("OAuth client %q created.\n  id:         %s\n  client_id:  %s\n", str(resp, "name"), str(resp, "id"), str(resp, "client_id"))
		if secret != "" {
			fmt.Printf("\nclient_secret (shown ONCE, store it now in a secret manager, never in git):\n\n%s\n", secret)
		} else {
			fmt.Println("  (public client: no secret; use PKCE)")
		}
		return nil
	},
}

var oauthListCmd = &cobra.Command{
	Use:   "list",
	Short: "List OAuth clients (secrets are never shown)",
	RunE: func(cmd *cobra.Command, args []string) error {
		list, err := oauthClientList()
		if err != nil {
			return err
		}
		if wantJSON(cmd) {
			for _, r := range list {
				delete(r, "client_secret")
			}
			return printJSON(list)
		}
		w := newTW()
		fmt.Fprintln(w, "ID\tCLIENT_ID\tNAME\tFLAGS\tREDIRECT URIS")
		for _, r := range list {
			var fl []string
			if str(r, "first_party") == "true" {
				fl = append(fl, "first-party")
			}
			if str(r, "public") == "true" {
				fl = append(fl, "public")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", short8(str(r, "id")), str(r, "client_id"), str(r, "name"), strings.Join(fl, ","), str(r, "redirect_uris"))
		}
		return w.Flush()
	},
}

var oauthUpdateCmd = &cobra.Command{
	Use:   "update ID|CLIENT_ID",
	Short: "Rename a client or replace its redirect URIs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := oauthResolve(args[0])
		if err != nil {
			return err
		}
		body := rec{}
		if n, _ := cmd.Flags().GetString("name"); n != "" {
			body["name"] = n
		}
		if cmd.Flags().Changed("redirect-uri") {
			uris, err := redirectURIs(cmd)
			if err != nil {
				return err
			}
			body["redirect_uris"] = uris
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to update: pass --name and/or --redirect-uri")
		}
		if err := apiClient.Patch(oauthPath+"/"+id, body, nil); err != nil {
			return platErr(err, "OAuth clients")
		}
		fmt.Println("Updated OAuth client", short8(id))
		return nil
	},
}

var oauthDeleteCmd = &cobra.Command{
	Use:   "delete ID|CLIENT_ID",
	Short: "Delete an OAuth client (apps using it stop signing in)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := oauthResolve(args[0])
		if err != nil {
			return err
		}
		if err := abortUnless(cmd, "Delete OAuth client "+short8(id)+"? Apps using it will stop working."); err != nil {
			return err
		}
		if err := apiClient.Delete(oauthPath + "/" + id); err != nil {
			return platErr(err, "OAuth clients")
		}
		fmt.Println("Deleted OAuth client", short8(id))
		return nil
	},
}

// ── capsule members / org access-mode ────────────────────────────────────────

var membersCmd = &cobra.Command{
	Use:   "members",
	Short: "Project members and roles (admin, deployer, viewer)",
}

func validRole(r string) (string, error) {
	r = strings.ToLower(strings.TrimSpace(r))
	switch r {
	case "admin", "deployer", "viewer":
		return r, nil
	}
	return "", fmt.Errorf("invalid --role %q (use admin, deployer or viewer)", r)
}

// resolveUser accepts a user ID or an email (resolved through the org members).
func resolveUser(cmd *cobra.Command, ref string) (string, error) {
	if !strings.Contains(ref, "@") {
		return ref, nil
	}
	org, err := resolveOrg(cmd)
	if err != nil {
		return "", err
	}
	v, err := getRaw("/api/v1/orgs/" + org + "/members")
	if err != nil {
		return "", platErr(err, "org members")
	}
	for _, m := range asList(v, "members") {
		if strings.EqualFold(str(m, "email"), ref) {
			if id := str(m, "user_id", "id"); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("no member of the org has email %q (see the org member list)", ref)
}

var membersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List effective project members with the origin of their access",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		v, err := getRaw(base + "/members")
		if err != nil {
			return platErr(err, "project members")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		w := newTW()
		fmt.Fprintln(w, "USER\tEMAIL\tROLE\tORIGIN")
		for _, m := range asList(v, "members") {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", short8(str(m, "user_id", "id")), str(m, "email"), str(m, "role"), str(m, "origin", "source"))
		}
		return w.Flush()
	},
}

var membersSetCmd = &cobra.Command{
	Use:     "set USER_ID|EMAIL --role admin|deployer|viewer",
	Short:   "Give a user a role on the project",
	Args:    cobra.ExactArgs(1),
	Example: `  capsule members set ana@example.com --role deployer`,
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _ := cmd.Flags().GetString("role")
		role, err := validRole(r)
		if err != nil {
			return err
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		uid, err := resolveUser(cmd, args[0])
		if err != nil {
			return err
		}
		if err := apiClient.Put(base+"/members/"+uid, rec{"role": role}, nil); err != nil {
			return platErr(err, "project members")
		}
		fmt.Printf("%s is now %s on the project.\n", args[0], role)
		return nil
	},
}

var membersRemoveCmd = &cobra.Command{
	Use:   "remove USER_ID|EMAIL",
	Short: "Remove a user's explicit project role",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		uid, err := resolveUser(cmd, args[0])
		if err != nil {
			return err
		}
		if err := abortUnless(cmd, "Remove "+args[0]+" from the project?"); err != nil {
			return err
		}
		if err := apiClient.Delete(base + "/members/" + uid); err != nil {
			return platErr(err, "project members")
		}
		fmt.Println("Removed", args[0], "from the project.")
		return nil
	},
}

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Organization settings",
}

var orgAccessModeCmd = &cobra.Command{
	Use:   "access-mode open|restricted",
	Short: "Set who sees projects: open (all org members) or restricted (only explicit members)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mode := strings.ToLower(args[0])
		if mode != "open" && mode != "restricted" {
			return fmt.Errorf("mode must be open or restricted")
		}
		org, err := resolveOrg(cmd)
		if err != nil {
			return err
		}
		if mode == "restricted" {
			if err := abortUnless(cmd, "Restricted mode hides every project from org members who are not explicitly added (capsule members set). Continue?"); err != nil {
				return err
			}
		}
		if err := apiClient.Patch("/api/v1/orgs/"+org, rec{"project_access_mode": mode}, nil); err != nil {
			return platErr(err, "project access modes")
		}
		fmt.Println("Project access mode:", mode)
		return nil
	},
}

func init() {
	oauthCreateCmd.Flags().String("name", "", "Client name")
	oauthCreateCmd.Flags().StringSlice("redirect-uri", nil, "Redirect URI (https, or http://localhost); repeatable")
	oauthCreateCmd.Flags().String("org-id", "", "Scope the client to an organization")
	oauthCreateCmd.Flags().Bool("first-party", false, "Mark as a first-party client")
	oauthCreateCmd.Flags().Bool("public", false, "Public client (no secret, PKCE)")
	oauthUpdateCmd.Flags().String("name", "", "New name")
	oauthUpdateCmd.Flags().StringSlice("redirect-uri", nil, "Replace the redirect URIs; repeatable")
	oauthDeleteCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	oauthClientsCmd.AddCommand(oauthCreateCmd, oauthListCmd, oauthUpdateCmd, oauthDeleteCmd)
	oauthCmd.AddCommand(oauthClientsCmd)
	rootCmd.AddCommand(oauthCmd)

	for _, c := range []*cobra.Command{membersListCmd, membersSetCmd, membersRemoveCmd} {
		addNetFlags(c)
	}
	membersSetCmd.Flags().String("role", "", "admin, deployer or viewer")
	membersRemoveCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	membersCmd.AddCommand(membersListCmd, membersSetCmd, membersRemoveCmd)
	rootCmd.AddCommand(membersCmd)

	addOrgOnlyFlag(orgAccessModeCmd)
	orgAccessModeCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	orgCmd.AddCommand(orgAccessModeCmd)
	rootCmd.AddCommand(orgCmd)
}
