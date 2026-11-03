package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ── capsule branch / capsule git branches|tags|protect ───────────────────────
// CLI.md 3.1 and 3.3. operationIds: branchesList branchCreate branchDelete
// tagsList tagCreate tagDelete protectionsList protectionCreate protectionPut
// protectionDelete protectionEvaluate.

func branchListRun(cmd *cobra.Command, args []string) error {
	p, err := gp(cmd, "/branches")
	if err != nil {
		return err
	}
	items, raw, err := fetchList(cmd, p)
	if err != nil {
		return err
	}
	if done, err := emit(cmd, raw); done || err != nil {
		return err
	}
	if quietIDs(cmd, items, "name") {
		return nil
	}
	t := newTable(cmd, "NAME", "PROTECTED", "AHEAD", "BEHIND", "LAST COMMIT", "UPDATED")
	for _, b := range items {
		prot := "no"
		if b["protected"] == true {
			prot = "yes"
			if id := sv(b, "protection_rule_id"); id != "" {
				prot += " (" + short8(id) + ")"
			}
		}
		name := sv(b, "name")
		if b["default"] == true {
			name += " *"
		}
		t.row(name, prot, sv(b, "ahead"), sv(b, "behind"), shortSHA(sv(b, "sha")), age(sv(b, "updated_at")))
	}
	return t.flush()
}

func branchCreateRun(cmd *cobra.Command, args []string) error {
	from, _ := cmd.Flags().GetString("from")
	if from == "" {
		rp, err := gp(cmd, "/repo")
		if err != nil {
			return err
		}
		var repo rec
		if _, err := apiDo("GET", rp, nil, &repo, nil); err != nil {
			return err
		}
		from = sv(repo, "default_branch")
		if from == "" {
			return usageErr("--from is required (the repository reports no default branch)")
		}
	}
	p, err := gp(cmd, "/branches")
	if err != nil {
		return err
	}
	var out rec
	if _, err := apiDo("POST", p, rec{"name": args[0], "from": from}, &out, nil); err != nil {
		return err
	}
	if done, err := emit(cmd, out); done || err != nil {
		return err
	}
	fmt.Printf("Branch %s created from %s (%s).\n", args[0], from, shortSHA(sv(out, "sha")))
	return nil
}

func branchDeleteRun(cmd *cobra.Command, args []string) error {
	if err := confirmOr(cmd, "Delete branch "+args[0]+"?"); err != nil {
		return err
	}
	p, err := gp(cmd, "/branches/"+seg(args[0]))
	if err != nil {
		return err
	}
	if _, err := apiDo("DELETE", p, nil, nil, nil); err != nil {
		return err
	}
	fmt.Printf("Branch %s deleted.\n", args[0])
	return nil
}

// newBranchList etc. build fresh command objects so the same behaviour can be
// mounted under `capsule branch` and `capsule git branches`.
func newBranchList() *cobra.Command {
	c := leaf("list", "List branches with protection state and ahead/behind", cobra.NoArgs, branchListRun)
	c.Aliases = []string{"ls"}
	addList(c)
	return c
}

func newBranchCreate() *cobra.Command {
	c := leaf("create NAME", "Create a branch", cobra.ExactArgs(1), branchCreateRun)
	c.Flags().String("from", "", "Branch, tag or SHA to start from (default: the default branch)")
	return c
}

func newBranchDelete() *cobra.Command {
	c := leaf("delete NAME", "Delete a branch (protected branches are refused with exit 5)", cobra.ExactArgs(1), branchDeleteRun)
	addYes(c)
	return c
}

func tagListRun(cmd *cobra.Command, args []string) error {
	p, err := gp(cmd, "/tags")
	if err != nil {
		return err
	}
	items, raw, err := fetchList(cmd, p)
	if err != nil {
		return err
	}
	if done, err := emit(cmd, raw); done || err != nil {
		return err
	}
	if quietIDs(cmd, items, "name") {
		return nil
	}
	t := newTable(cmd, "TAG", "SHA", "CREATED", "MESSAGE")
	for _, tg := range items {
		t.row(sv(tg, "name"), shortSHA(sv(tg, "sha")), age(sv(tg, "created_at")), truncate(sv(tg, "message"), 60))
	}
	return t.flush()
}

func init() {
	// capsule branch (CLI.md 3.3)
	protect := leaf("protect NAME", "Protect a branch (same flags as `git protect set`)", cobra.ExactArgs(1), protectSetRun)
	addProtectFlags(protect)
	unprotect := leaf("unprotect NAME", "Remove the protection rule of a branch", cobra.ExactArgs(1), protectRemoveRun)
	addYes(unprotect)
	branchGroup := group("branch", "Branches: list, create, delete, protect", newBranchList(), newBranchCreate(), newBranchDelete(), protect, unprotect)
	rootCmd.AddCommand(branchGroup)

	// capsule git branches list|create|delete (the existing `git branches` keeps its own listing)
	gitBranchesCmd.AddCommand(newBranchList(), newBranchCreate(), newBranchDelete())

	// capsule git tags list|create|delete
	tagsList := leaf("list", "List tags", cobra.NoArgs, tagListRun)
	tagsList.Aliases = []string{"ls"}
	addList(tagsList)
	tagsCreate := leaf("create NAME", "Create a tag", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetString("target")
		if target == "" {
			return usageErr("--target (branch or SHA) is required")
		}
		body := rec{"name": args[0], "target": target}
		if m, _ := cmd.Flags().GetString("message"); m != "" {
			body["message"] = m
		}
		p, err := gp(cmd, "/tags")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, body, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Tag %s created at %s.\n", args[0], shortSHA(sv(out, "sha")))
		return nil
	})
	tagsCreate.Flags().String("target", "", "Branch or commit SHA to tag")
	tagsCreate.Flags().StringP("message", "m", "", "Annotation message")
	tagsDelete := leaf("delete NAME", "Delete a tag", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := confirmOr(cmd, "Delete tag "+args[0]+"?"); err != nil {
			return err
		}
		p, err := gp(cmd, "/tags/"+seg(args[0]))
		if err != nil {
			return err
		}
		if _, err := apiDo("DELETE", p, nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("Tag %s deleted.\n", args[0])
		return nil
	})
	addYes(tagsDelete)
	tags := group("tags", "Tags: list, create, delete", tagsList, tagsCreate, tagsDelete)
	gitCmd.AddCommand(tags)
}
