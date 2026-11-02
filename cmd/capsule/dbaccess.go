package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type dbItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Engine string `json:"engine"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	DBName string `json:"db_name"`
	Status string `json:"status"`
}

type accessRule struct {
	ID          string `json:"id"`
	CIDR        string `json:"cidr"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

type accessResp struct {
	Rules []accessRule `json:"rules"`
	Host  string       `json:"host"`
	Port  int          `json:"port"`
	TLS   bool         `json:"tls"`
}

func dbBase(orgID, projectID string) string {
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s/databases", orgID, projectID)
}

// pickDB matches a database by exact name or ID first, then by ID prefix.
func pickDB(dbs []dbItem, ref string) (*dbItem, error) {
	for i := range dbs {
		if dbs[i].Name == ref || dbs[i].ID == ref {
			return &dbs[i], nil
		}
	}
	var m []*dbItem
	for i := range dbs {
		if strings.HasPrefix(dbs[i].ID, ref) {
			m = append(m, &dbs[i])
		}
	}
	switch len(m) {
	case 0:
		return nil, fmt.Errorf("no database with name or id %q (see: capsule db list)", ref)
	case 1:
		return m[0], nil
	}
	return nil, fmt.Errorf("database id prefix %q is ambiguous (%d match); use more characters", ref, len(m))
}

func resolveDB(orgID, projectID, ref string) (*dbItem, error) {
	var resp struct {
		Data []dbItem `json:"data"`
	}
	if err := apiClient.Get(dbBase(orgID, projectID), &resp); err != nil {
		return nil, err
	}
	return pickDB(resp.Data, ref)
}

// pickRule matches a rule by CIDR/IP (when the ref looks like one) or by ID prefix.
func pickRule(rules []accessRule, ref string) (*accessRule, error) {
	if strings.ContainsAny(ref, "./:") {
		c, err := normalizeCIDR(ref)
		if err != nil {
			return nil, err
		}
		for i := range rules {
			if rules[i].CIDR == c {
				return &rules[i], nil
			}
		}
		return nil, fmt.Errorf("no rule for %s", c)
	}
	var m []*accessRule
	for i := range rules {
		if strings.HasPrefix(rules[i].ID, ref) {
			m = append(m, &rules[i])
		}
	}
	switch len(m) {
	case 0:
		return nil, fmt.Errorf("no rule with id starting %q", ref)
	case 1:
		return m[0], nil
	}
	return nil, fmt.Errorf("rule id prefix %q is ambiguous (%d match); use more characters", ref, len(m))
}

func id8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func printAccess(d *dbItem, a *accessResp) {
	if len(a.Rules) == 0 {
		fmt.Println("No access rules: nothing can reach this database from outside.")
		fmt.Println("Add yours with: capsule db access add " + d.Name + " --my-ip")
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tCIDR\tDESCRIPTION")
		for _, r := range a.Rules {
			fmt.Fprintf(w, "%s\t%s\t%s\n", id8(r.ID), r.CIDR, r.Description)
		}
		w.Flush()
	}
	host, port := a.Host, a.Port
	if host == "" {
		host, port = d.Host, d.Port
	}
	if host != "" {
		name := d.DBName
		if name == "" {
			name = "<dbname>"
		}
		fmt.Printf("\nConnection (the password is never printed here):\n")
		fmt.Printf("  postgresql://<user>:<password>@%s:%d/%s?sslmode=require\n", host, port, name)
		fmt.Println("  TLS is required: always connect with sslmode=require.")
	}
}

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Manage project databases",
}

var dbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the databases of a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		var resp struct {
			Data []dbItem `json:"data"`
		}
		if err := apiClient.Get(dbBase(orgID, projectID), &resp); err != nil {
			return err
		}
		if len(resp.Data) == 0 {
			fmt.Println("No databases in this project.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tENGINE\tHOST:PORT\tSTATUS")
		for _, d := range resp.Data {
			hp := "-"
			if d.Host != "" {
				hp = fmt.Sprintf("%s:%d", d.Host, d.Port)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", id8(d.ID), d.Name, d.Engine, hp, d.Status)
		}
		return w.Flush()
	},
}

var dbAccessCmd = &cobra.Command{
	Use:   "access",
	Short: "Control which IPs can reach a database (allowlist)",
	Long: `Control which IP ranges can connect to a database from outside.
<db> accepts the short ID printed by "capsule db list" or the database name.
Connections require TLS (sslmode=require). Tokens with scope "deploy" cannot use this.`,
}

func dbAccessCtx(cmd *cobra.Command, ref string) (string, *dbItem, error) {
	orgID, projectID, err := resolveOrgProject(cmd)
	if err != nil {
		return "", nil, err
	}
	d, err := resolveDB(orgID, projectID, ref)
	if err != nil {
		return "", nil, friendlyNetErr(err)
	}
	return dbBase(orgID, projectID) + "/" + d.ID + "/access", d, nil
}

func showAccess(path string, d *dbItem) error {
	var a accessResp
	if err := apiClient.Get(path, &a); err != nil {
		return friendlyNetErr(err)
	}
	printAccess(d, &a)
	return nil
}

var dbAccessListCmd = &cobra.Command{
	Use:     "list <db>",
	Short:   "Show access rules and the connection string",
	Args:    cobra.ExactArgs(1),
	Example: `  capsule db access list mydb`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, d, err := dbAccessCtx(cmd, args[0])
		if err != nil {
			return err
		}
		return showAccess(path, d)
	},
}

// publicGuard enforces the explicit-consent rule for /0 ranges. It returns
// the normalized list and whether any entry is public.
func publicGuard(cmd *cobra.Command, cidrs []string) ([]string, bool, error) {
	allow, _ := cmd.Flags().GetBool("allow-public")
	norm, err := normalizeCIDRs(cidrs, allow)
	if err != nil {
		return nil, false, err
	}
	pub := false
	for _, c := range norm {
		if isPublicCIDR(c) {
			pub = true
		}
	}
	if pub && !confirmOrYes(cmd, "This exposes the database to the ENTIRE internet. Continue?") {
		return nil, false, fmt.Errorf("aborted")
	}
	return norm, pub, nil
}

var dbAccessAddCmd = &cobra.Command{
	Use:   "add <db>",
	Short: "Allow an IP or CIDR to connect",
	Args:  cobra.ExactArgs(1),
	Example: `  capsule db access add mydb --cidr 203.0.113.7/32 --desc "office"
  capsule db access add mydb --my-ip`,
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := cidrsFromFlags(cmd)
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			return fmt.Errorf("use --cidr <ip/cidr> or --my-ip")
		}
		norm, pub, err := publicGuard(cmd, raw)
		if err != nil {
			return err
		}
		desc, _ := cmd.Flags().GetString("desc")
		path, d, err := dbAccessCtx(cmd, args[0])
		if err != nil {
			return err
		}
		for _, c := range norm {
			body := map[string]any{"cidr": c, "description": desc}
			if pub {
				body["allow_public"] = true
			}
			if err := apiClient.Post(path, body, nil); err != nil {
				return friendlyNetErr(err)
			}
			fmt.Printf("Allowed %s on %s\n", c, d.Name)
		}
		return showAccess(path, d)
	},
}

var dbAccessRemoveCmd = &cobra.Command{
	Use:   "remove <db> [rule-id]",
	Short: "Remove a rule by short ID or --cidr",
	Args:  cobra.RangeArgs(1, 2),
	Example: `  capsule db access remove mydb --cidr 203.0.113.7/32
  capsule db access remove mydb 3fa85f64`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ref, _ := cmd.Flags().GetString("cidr")
		if len(args) == 2 {
			ref = args[1]
		}
		if ref == "" {
			return fmt.Errorf("give a rule id or --cidr")
		}
		path, d, err := dbAccessCtx(cmd, args[0])
		if err != nil {
			return err
		}
		var a accessResp
		if err := apiClient.Get(path, &a); err != nil {
			return friendlyNetErr(err)
		}
		r, err := pickRule(a.Rules, ref)
		if err != nil {
			return err
		}
		if err := apiClient.Delete(path + "/" + r.ID); err != nil {
			return friendlyNetErr(err)
		}
		fmt.Printf("Removed %s (%s) from %s\n", r.CIDR, id8(r.ID), d.Name)
		return nil
	},
}

var dbAccessSetCmd = &cobra.Command{
	Use:     "set <db>",
	Short:   "Replace all rules with the given --cidr list",
	Args:    cobra.ExactArgs(1),
	Example: `  capsule db access set mydb --cidr 203.0.113.7/32 --cidr 198.51.100.0/24`,
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := cidrsFromFlags(cmd)
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			return fmt.Errorf("use --cidr (repeatable) or --my-ip")
		}
		norm, pub, err := publicGuard(cmd, raw)
		if err != nil {
			return err
		}
		desc, _ := cmd.Flags().GetString("desc")
		items := make([]map[string]string, 0, len(norm))
		for _, c := range norm {
			items = append(items, map[string]string{"cidr": c, "description": desc})
		}
		body := map[string]any{"cidrs": items}
		if pub {
			body["allow_public"] = true
		}
		path, d, err := dbAccessCtx(cmd, args[0])
		if err != nil {
			return err
		}
		if err := apiClient.Put(path, body, nil); err != nil {
			return friendlyNetErr(err)
		}
		fmt.Printf("Access rules of %s replaced (%d)\n", d.Name, len(norm))
		return showAccess(path, d)
	},
}

func init() {
	addNetFlags(dbListCmd)
	for _, c := range []*cobra.Command{dbAccessListCmd, dbAccessAddCmd, dbAccessRemoveCmd, dbAccessSetCmd} {
		addNetFlags(c)
	}
	for _, c := range []*cobra.Command{dbAccessAddCmd, dbAccessSetCmd} {
		c.Flags().StringSlice("cidr", nil, "IP or CIDR (a bare IP becomes /32); repeatable")
		c.Flags().Bool("my-ip", false, "Use your current public IP (/32)")
		c.Flags().String("desc", "", "Description for the rule(s)")
		c.Flags().Bool("allow-public", false, "Allow 0.0.0.0/0 (whole internet); asks for confirmation")
		c.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	}
	dbAccessRemoveCmd.Flags().String("cidr", "", "Remove the rule with this CIDR")
	dbAccessCmd.AddCommand(dbAccessListCmd, dbAccessAddCmd, dbAccessRemoveCmd, dbAccessSetCmd)
	dbCmd.AddCommand(dbListCmd, dbAccessCmd)
	rootCmd.AddCommand(dbCmd)
}
