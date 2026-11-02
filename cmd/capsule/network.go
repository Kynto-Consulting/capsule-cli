package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

func networkPath(orgID, projectID string) string {
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s/network", orgID, projectID)
}

func getAllowlist(path string) ([]string, error) {
	var r struct {
		IPAllowlist []string `json:"ip_allowlist"`
	}
	if err := apiClient.Get(path, &r); err != nil {
		return nil, friendlyNetErr(err)
	}
	return r.IPAllowlist, nil
}

func putAllowlist(path string, list []string) error {
	if list == nil {
		list = []string{}
	}
	return friendlyNetErr(apiClient.Put(path, map[string]any{"ip_allowlist": list}, nil))
}

func printAllowlist(list []string) {
	if len(list) == 0 {
		fmt.Println("Allowlist is empty: the project is PUBLIC (reachable from any IP).")
		return
	}
	fmt.Println("Only these ranges can reach the project:")
	for _, c := range list {
		fmt.Println("  " + c)
	}
}

// mergeCIDRs returns the sorted union of base and add.
func mergeCIDRs(base, add []string) []string {
	m := map[string]bool{}
	for _, c := range base {
		m[c] = true
	}
	for _, c := range add {
		m[c] = true
	}
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// subtractCIDRs removes del from base; missing lists values not found.
func subtractCIDRs(base, del []string) (kept, missing []string) {
	d := map[string]bool{}
	for _, c := range del {
		d[c] = true
	}
	found := map[string]bool{}
	for _, c := range base {
		if d[c] {
			found[c] = true
			continue
		}
		kept = append(kept, c)
	}
	for _, c := range del {
		if !found[c] {
			missing = append(missing, c)
		}
	}
	return
}

var networkCmd = &cobra.Command{
	Use:   "network",
	Short: "Restrict which IPs can reach the project (allowlist)",
	Long: `Manage the project's IP allowlist. An empty allowlist means the project is public.
Tokens with scope "deploy" cannot use this.`,
}

func networkCtx(cmd *cobra.Command) (string, error) {
	orgID, projectID, err := resolveOrgProject(cmd)
	if err != nil {
		return "", err
	}
	return networkPath(orgID, projectID), nil
}

func cidrsOrErr(cmd *cobra.Command) ([]string, error) {
	raw, err := cidrsFromFlags(cmd)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("use --cidr <ip/cidr> or --my-ip")
	}
	// Whole-internet ranges are never valid here: use `network clear`.
	return normalizeCIDRs(raw, false)
}

var networkShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the project allowlist",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := networkCtx(cmd)
		if err != nil {
			return err
		}
		l, err := getAllowlist(p)
		if err != nil {
			return err
		}
		printAllowlist(l)
		return nil
	},
}

var networkAllowCmd = &cobra.Command{
	Use:   "allow",
	Short: "Add IPs/CIDRs to the allowlist",
	Example: `  capsule network allow --cidr 203.0.113.0/24
  capsule network allow --my-ip`,
	RunE: func(cmd *cobra.Command, args []string) error {
		add, err := cidrsOrErr(cmd)
		if err != nil {
			return err
		}
		p, err := networkCtx(cmd)
		if err != nil {
			return err
		}
		cur, err := getAllowlist(p)
		if err != nil {
			return err
		}
		next := mergeCIDRs(cur, add)
		if err := putAllowlist(p, next); err != nil {
			return err
		}
		printAllowlist(next)
		return nil
	},
}

var networkDenyCmd = &cobra.Command{
	Use:     "deny",
	Short:   "Remove IPs/CIDRs from the allowlist",
	Example: `  capsule network deny --cidr 203.0.113.0/24`,
	RunE: func(cmd *cobra.Command, args []string) error {
		del, err := cidrsOrErr(cmd)
		if err != nil {
			return err
		}
		p, err := networkCtx(cmd)
		if err != nil {
			return err
		}
		cur, err := getAllowlist(p)
		if err != nil {
			return err
		}
		next, missing := subtractCIDRs(cur, del)
		for _, m := range missing {
			stderrf("note: %s was not in the allowlist\n", m)
		}
		if len(next) == 0 && len(cur) > 0 {
			if !confirmOrYes(cmd, "This empties the allowlist and makes the project PUBLIC. Continue?") {
				return fmt.Errorf("aborted")
			}
		}
		if err := putAllowlist(p, next); err != nil {
			return err
		}
		printAllowlist(next)
		return nil
	},
}

var networkSetCmd = &cobra.Command{
	Use:     "set",
	Short:   "Replace the allowlist",
	Example: `  capsule network set --cidr 203.0.113.0/24 --cidr 198.51.100.7`,
	RunE: func(cmd *cobra.Command, args []string) error {
		l, err := cidrsOrErr(cmd)
		if err != nil {
			return err
		}
		p, err := networkCtx(cmd)
		if err != nil {
			return err
		}
		if err := putAllowlist(p, l); err != nil {
			return err
		}
		printAllowlist(l)
		return nil
	},
}

var networkClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Empty the allowlist (project becomes public)",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := networkCtx(cmd)
		if err != nil {
			return err
		}
		if !confirmOrYes(cmd, "This makes the project PUBLIC (reachable from any IP). Continue?") {
			return fmt.Errorf("aborted")
		}
		if err := putAllowlist(p, nil); err != nil {
			return err
		}
		printAllowlist(nil)
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{networkShowCmd, networkAllowCmd, networkDenyCmd, networkSetCmd, networkClearCmd} {
		addNetFlags(c)
	}
	for _, c := range []*cobra.Command{networkAllowCmd, networkDenyCmd, networkSetCmd} {
		c.Flags().StringSlice("cidr", nil, "IP or CIDR (a bare IP becomes /32); repeatable")
		c.Flags().Bool("my-ip", false, "Use your current public IP (/32)")
	}
	networkDenyCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	networkClearCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	networkCmd.AddCommand(networkShowCmd, networkAllowCmd, networkDenyCmd, networkSetCmd, networkClearCmd)
	rootCmd.AddCommand(networkCmd)
}
