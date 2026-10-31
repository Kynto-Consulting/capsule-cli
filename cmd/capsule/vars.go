package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// Account (organization) variables: shared by every project of the org.
// Precedence at deploy time: deploy (--env) > project (capsule env) > account (capsule vars).

func orgEnvPath(orgID string) string { return fmt.Sprintf("/api/v1/orgs/%s/env", orgID) }

// resolveOrg: --org flag > CAPSULE_ORG_ID > .capsule.json.
func resolveOrg(cmd *cobra.Command) (string, error) {
	if v, _ := cmd.Flags().GetString("org"); v != "" {
		return v, nil
	}
	if v, _ := envOrgProject(); v != "" {
		return v, nil
	}
	cwd, _ := os.Getwd()
	if d := os.Getenv("CAPSULE_CWD"); d != "" {
		cwd = d
	}
	if pc, _, err := config.FindProjectConfig(cwd); err == nil && pc.OrgID != "" {
		return pc.OrgID, nil
	}
	return "", fmt.Errorf("--org is required (or set CAPSULE_ORG_ID, or link directory with: capsule link)")
}

func normalizeScope(scope string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(scope))
	switch s {
	case "build", "runtime", "both":
		return s, nil
	}
	return "", fmt.Errorf("invalid --scope %q (use build, runtime or both)", scope)
}

var varsCmd = &cobra.Command{
	Use:   "vars",
	Short: "Manage account (organization) variables shared by all projects",
	Long: `Manage account-level variables. They apply to every project in the organization.
Precedence: deploy variables (capsule deploy --env) > project variables (capsule env) > account variables.`,
}

var varsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List account variables",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, err := resolveOrg(cmd)
		if err != nil {
			return err
		}
		path := orgEnvPath(orgID)
		reveal, _ := cmd.Flags().GetBool("reveal")
		if reveal {
			path += "?" + url.Values{"reveal": {"true"}}.Encode()
		}
		var vars []envVarItem
		if err := apiClient.Get(path, &vars); err != nil {
			return fmt.Errorf("fetching account variables: %w", err)
		}
		if len(vars) == 0 {
			fmt.Println("No account variables. Set one with: capsule vars set KEY=VALUE")
			return nil
		}
		sort.Slice(vars, func(i, j int) bool { return vars[i].Key < vars[j].Key })
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "KEY\tVALUE\tSCOPE\tSECRET")
		for _, v := range vars {
			val := v.Value
			if v.IsSecret && !reveal {
				val = "***"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Key, val, v.Scope, secretLabel(v.IsSecret))
		}
		return w.Flush()
	},
}

var varsSetCmd = &cobra.Command{
	Use:   "set KEY=VALUE [KEY2=VALUE2 ...]",
	Short: "Create or update account variables",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, err := resolveOrg(cmd)
		if err != nil {
			return err
		}
		isSecret, _ := cmd.Flags().GetBool("secret")
		scopeFlag, _ := cmd.Flags().GetString("scope")
		scope, err := normalizeScope(scopeFlag)
		if err != nil {
			return err
		}
		// Validate everything first so a typo does not leave a half-applied set.
		type kv struct{ k, v string }
		var items []kv
		for _, arg := range args {
			k, v, err := parseKeyVal(arg)
			if err != nil {
				return err
			}
			items = append(items, kv{strings.ToUpper(k), v})
		}
		for _, it := range items {
			body := map[string]interface{}{
				"key": it.k, "value": it.v, "is_secret": isSecret, "scope": scope,
			}
			var result envVarItem
			if err := apiClient.Put(orgEnvPath(orgID), body, &result); err != nil {
				return fmt.Errorf("setting %s: %w", it.k, err)
			}
			fmt.Printf("Set %s (%s)\n", it.k, scope)
		}
		return nil
	},
}

var varsDeleteCmd = &cobra.Command{
	Use:   "delete KEY",
	Short: "Delete an account variable",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, err := resolveOrg(cmd)
		if err != nil {
			return err
		}
		key := strings.ToUpper(strings.TrimSpace(args[0]))
		if !envKeyRe.MatchString(key) {
			return fmt.Errorf("invalid variable name %q", key)
		}
		if err := apiClient.Delete(orgEnvPath(orgID) + "/" + key); err != nil {
			return fmt.Errorf("deleting account variable: %w", err)
		}
		fmt.Printf("Deleted %s\n", key)
		return nil
	},
}

func init() {
	for _, sub := range []*cobra.Command{varsListCmd, varsSetCmd, varsDeleteCmd} {
		sub.Flags().String("org", "", "Org ID (overrides CAPSULE_ORG_ID and .capsule.json)")
	}
	varsListCmd.Flags().Bool("reveal", false, "Show secret values (requires permission)")
	varsSetCmd.Flags().Bool("secret", false, "Mark value as a secret (masked in list output)")
	varsSetCmd.Flags().String("scope", "runtime", "Scope: build | runtime | both")
	varsCmd.AddCommand(varsListCmd, varsSetCmd, varsDeleteCmd)
	rootCmd.AddCommand(varsCmd)
}
