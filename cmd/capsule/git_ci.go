package main

import (
	"fmt"
	"regexp"

	"github.com/spf13/cobra"
)

// ── capsule ci secrets | vars | runners ──────────────────────────────────────
// CLI.md 3.4. operationIds: ciSecretsList ciSecretPut ciSecretDelete
// ciVarsList ciVarPut ciVarDelete orgCiSecretsList orgCiSecretPut
// orgCiSecretDelete orgCiVarsList orgCiVarPut orgCiVarDelete runnersList.
// Secret values come from stdin or --from-env, never from argv. `--org` is the
// org id as everywhere else; `--org-wide` selects the org-level store.

var ciNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ciBase returns the .../git path for the project store or the org store.
func ciBase(cmd *cobra.Command) (string, error) {
	if w, _ := cmd.Flags().GetBool("org-wide"); w {
		return op(cmd, "/git")
	}
	return gp(cmd, "")
}

func addScopeFlags(c *cobra.Command) {
	c.Flags().Bool("org-wide", false, "Use the organization-level store instead of the project's")
}

func ciKind(kind, plural string) {
	isSecret := kind == "secret"
	list := leaf("list", "List "+plural+" (names only for secrets)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		base, err := ciBase(cmd)
		if err != nil {
			return err
		}
		var raw any
		if _, err := apiDo("GET", base+"/"+plural, nil, &raw, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		items, _ := pageOf(raw)
		if quietIDs(cmd, items, "name") {
			return nil
		}
		if isSecret {
			t := newTable(cmd, "NAME", "UPDATED")
			for _, s := range items {
				t.row(sv(s, "name"), age(sv(s, "updated_at")))
			}
			return t.flush()
		}
		t := newTable(cmd, "NAME", "VALUE", "SCOPE", "UPDATED")
		for _, v := range items {
			t.row(sv(v, "name"), truncate(sv(v, "value"), 50), sv(v, "scope"), age(sv(v, "updated_at")))
		}
		return t.flush()
	})
	list.Flags().BoolP("quiet", "q", false, "Print only names")
	addScopeFlags(list)

	set := leaf("set NAME", "Create or replace a "+kind+" (value from stdin or --from-env)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		if !ciNameRe.MatchString(args[0]) {
			return usageErr("invalid name %q: letters, digits and underscore, not starting with a digit", args[0])
		}
		val, err := readSecretValue(cmd, !isSecret)
		if err != nil {
			return err
		}
		base, err := ciBase(cmd)
		if err != nil {
			return err
		}
		if _, err := apiDo("PUT", base+"/"+plural+"/"+seg(args[0]), rec{"value": val}, nil, nil); err != nil {
			return err
		}
		fmt.Printf("%s %s saved.\n", map[bool]string{true: "Secret", false: "Variable"}[isSecret], args[0])
		return nil
	})
	set.Flags().String("from-env", "", "Read the value from this environment variable")
	if !isSecret {
		set.Flags().String("value", "", "Variable value (variables are not secret; use stdin or --from-env for secrets)")
	}
	addScopeFlags(set)

	del := leaf("delete NAME", "Delete a "+kind, cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := confirmOr(cmd, "Delete "+kind+" "+args[0]+"?"); err != nil {
			return err
		}
		base, err := ciBase(cmd)
		if err != nil {
			return err
		}
		if _, err := apiDo("DELETE", base+"/"+plural+"/"+seg(args[0]), nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("%s %s deleted.\n", map[bool]string{true: "Secret", false: "Variable"}[isSecret], args[0])
		return nil
	})
	addYes(del)
	addScopeFlags(del)

	name := plural
	g := group(name, "Actions "+plural+" of the project or organization", list, set, del)
	if plural == "variables" {
		g.Aliases = []string{"vars"}
	}
	ciCmd.AddCommand(g)
}

func init() {
	ciKind("secret", "secrets")
	ciKind("variable", "variables")
	runners := orgLeaf("runners", "List the organization's Actions runners", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := op(cmd, "/git/runners")
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
		t := newTable(cmd, "ID", "NAME", "STATUS", "LABELS", "TRUSTED")
		for _, r := range items {
			t.row(sv(r, "id"), sv(r, "name"), sv(r, "status"), sv(r, "labels"), yn(r["trusted"] == true))
		}
		return t.flush()
	})
	runners.Flags().BoolP("quiet", "q", false, "Print only IDs")
	ciCmd.AddCommand(runners)
}
