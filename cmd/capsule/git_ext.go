package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ── capsule git: extension (CLI.md 3.1) ──────────────────────────────────────
// operationIds: repoGet repoProvision repoUpdate repoTree repoBlob repoCompare
// commitGet commitChecks codeownersGet mirrorGet mirrorPut mirrorDelete
// mirrorSync importCreate importGet gitStatus activityList insights
// gitReconcile; protections are handled below.

func protectionBody(cmd *cobra.Command) (rec, error) {
	f := cmd.Flags()
	b := rec{}
	boolFlag := func(flag, key string) {
		if f.Changed(flag) {
			v, _ := f.GetBool(flag)
			b[key] = v
		}
	}
	boolFlag("require-pr", "require_pull_request")
	boolFlag("dismiss-stale", "dismiss_stale_approvals")
	boolFlag("block-rejected", "block_on_rejected_reviews")
	boolFlag("codeowners", "require_code_owner_review")
	boolFlag("up-to-date", "require_up_to_date")
	boolFlag("signed", "require_signed_commits")
	boolFlag("linear", "require_linear_history")
	boolFlag("enforce-admins", "enforce_for_admins")
	boolFlag("block-on-security", "block_on_security_policy")
	boolFlag("delete-branch-on-merge", "delete_branch_on_merge")
	if f.Changed("no-force-push") && f.Changed("allow-force-push") {
		return nil, usageErr("--no-force-push and --allow-force-push conflict")
	}
	if f.Changed("no-force-push") {
		b["block_force_push"] = true
	}
	if f.Changed("allow-force-push") {
		b["block_force_push"] = false
	}
	if f.Changed("no-delete") && f.Changed("allow-delete") {
		return nil, usageErr("--no-delete and --allow-delete conflict")
	}
	if f.Changed("no-delete") {
		b["block_deletion"] = true
	}
	if f.Changed("allow-delete") {
		b["block_deletion"] = false
	}
	if f.Changed("approvals") {
		n, _ := f.GetInt("approvals")
		if n < 0 || n > 10 {
			return nil, usageErr("--approvals must be between 0 and 10")
		}
		b["required_approvals"] = n
	}
	if f.Changed("checks") {
		b["required_checks"] = csvFlag(cmd, "checks")
	}
	for flag, key := range map[string]string{"push-roles": "push_allow_roles", "merge-roles": "merge_allow_roles"} {
		if f.Changed(flag) {
			roles := csvFlag(cmd, flag)
			for _, r := range roles {
				if r != "admin" && r != "deployer" {
					return nil, usageErr("--%s accepts admin and deployer, got %q", flag, r)
				}
			}
			b[key] = roles
		}
	}
	return b, nil
}

func addProtectFlags(c *cobra.Command) {
	f := c.Flags()
	f.Bool("require-pr", false, "Require a pull request to change the branch")
	f.Int("approvals", 0, "Required approvals (0-10)")
	f.Bool("dismiss-stale", false, "Dismiss approvals when new commits arrive")
	f.Bool("block-rejected", false, "Block merging while a review requests changes")
	f.Bool("codeowners", false, "Require approval from CODEOWNERS")
	f.StringSlice("checks", nil, "Required check contexts (comma separated, globs allowed)")
	f.Bool("up-to-date", false, "Require the branch to be up to date with the base")
	f.Bool("signed", false, "Require signed commits")
	f.Bool("linear", false, "Require linear history")
	f.Bool("no-force-push", false, "Block force pushes")
	f.Bool("allow-force-push", false, "Allow force pushes")
	f.Bool("no-delete", false, "Block deleting the branch")
	f.Bool("allow-delete", false, "Allow deleting the branch")
	f.StringSlice("push-roles", nil, "Roles allowed to push: admin,deployer")
	f.StringSlice("merge-roles", nil, "Roles allowed to merge: admin,deployer")
	f.Bool("enforce-admins", false, "Apply the rule to admins too")
	f.Bool("block-on-security", false, "Block merging when the security policy blocks")
	f.Bool("delete-branch-on-merge", false, "Delete the head branch after merging")
}

func listProtections(cmd *cobra.Command) ([]rec, string, error) {
	p, err := gp(cmd, "/protections")
	if err != nil {
		return nil, "", err
	}
	var raw any
	if _, err := apiDo("GET", p, nil, &raw, nil); err != nil {
		return nil, "", err
	}
	items, _ := pageOf(raw)
	return items, p, nil
}

// findRule matches a rule by pattern or id prefix.
func findRule(items []rec, ref string) (rec, error) {
	for _, r := range items {
		if sv(r, "pattern") == ref {
			return r, nil
		}
	}
	return pickByPrefix(items, ref, "protection rule", "id")
}

func protectSetRun(cmd *cobra.Command, args []string) error {
	body, err := protectionBody(cmd)
	if err != nil {
		return err
	}
	items, base, err := listProtections(cmd)
	if err != nil {
		return err
	}
	var out rec
	var existing rec
	for _, r := range items {
		if sv(r, "pattern") == args[0] {
			existing = r
		}
	}
	if existing != nil {
		merged := rec{}
		for k, v := range existing {
			switch k {
			case "id", "created_at", "forgejo_synced", "sync_error":
			default:
				merged[k] = v
			}
		}
		for k, v := range body {
			merged[k] = v
		}
		if _, err := apiDo("PUT", base+"/"+seg(sv(existing, "id")), merged, &out, nil); err != nil {
			return err
		}
	} else {
		body["pattern"] = args[0]
		if _, err := apiDo("POST", base, body, &out, nil); err != nil {
			return err
		}
	}
	if done, err := emit(cmd, out); done || err != nil {
		return err
	}
	synced := out["forgejo_synced"] == true
	fmt.Printf("Rule %q saved", args[0])
	if synced {
		fmt.Println(" and synced with Forgejo (forgejo_synced=true)")
	} else {
		fmt.Println(" in Capsule; the sync with Forgejo is pending")
		if e := sv(out, "sync_error"); e != "" {
			fmt.Println("  sync error:", e)
		}
	}
	return nil
}

func protectRemoveRun(cmd *cobra.Command, args []string) error {
	items, base, err := listProtections(cmd)
	if err != nil {
		return err
	}
	r, err := findRule(items, args[0])
	if err != nil {
		return usageErr("%v", err)
	}
	if err := confirmOr(cmd, "Remove the protection rule "+sv(r, "pattern")+"?"); err != nil {
		return err
	}
	if _, err := apiDo("DELETE", base+"/"+seg(sv(r, "id")), nil, nil, nil); err != nil {
		return err
	}
	fmt.Printf("Rule %q removed.\n", sv(r, "pattern"))
	return nil
}

// cloneCmd copies a command (use, flags and behaviour) so the same operation
// can live under two parents.
func cloneCmd(c *cobra.Command, use string) *cobra.Command {
	n := &cobra.Command{
		Use: use, Short: c.Short, Long: c.Long, Example: c.Example, Args: c.Args,
		RunE: c.RunE, SilenceUsage: true, Aliases: c.Aliases,
	}
	c.Flags().VisitAll(func(f *pflag.Flag) {
		nf := *f
		n.Flags().AddFlag(&nf)
	})
	return n
}

func init() {
	// ── tree / cat / compare ─────────────────────────────────────────────────
	tree := leaf("tree [PATH]", "List files of a directory at a ref", cobra.MaximumNArgs(1), func(cmd *cobra.Command, args []string) error {
		ref, _ := cmd.Flags().GetString("ref")
		path := ""
		if len(args) == 1 {
			path = strings.TrimPrefix(args[0], "/")
		}
		p, err := gp(cmd, "/tree"+q("ref", ref, "path", path))
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
		if quietIDs(cmd, items, "path") {
			return nil
		}
		t := newTable(cmd, "TYPE", "SIZE", "PATH")
		for _, e := range items {
			t.row(sv(e, "type"), sv(e, "size"), sv(e, "path", "name"))
		}
		return t.flush()
	})
	tree.Flags().String("ref", "", "Branch, tag or SHA (default: the default branch)")
	addList(tree)

	cat := leaf("cat PATH", "Print a file at a ref", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		ref, _ := cmd.Flags().GetString("ref")
		p, err := gp(cmd, "/blob"+q("ref", ref, "path", strings.TrimPrefix(args[0], "/")))
		if err != nil {
			return err
		}
		var blob rec
		if _, err := apiDo("GET", p, nil, &blob, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, blob); done || err != nil {
			return err
		}
		content := sv(blob, "content")
		if blob["binary"] == true && sv(blob, "encoding") != "base64" {
			return usageErr("%s is a binary file; use --output json to get it base64-encoded", args[0])
		}
		if sv(blob, "encoding") == "base64" {
			b, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return fmt.Errorf("decoding file content: %w", err)
			}
			_, err = os.Stdout.Write(b)
			return err
		}
		fmt.Print(content)
		if blob["truncated"] == true {
			stderrf("\n(file truncated by the server)\n")
		}
		return nil
	})
	cat.Flags().String("ref", "", "Branch, tag or SHA (default: the default branch)")

	compare := leaf("compare BASE HEAD", "Compare two refs: ahead/behind, commits and changed files", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/compare"+q("base", args[0], "head", args[1]))
		if err != nil {
			return err
		}
		var cmp rec
		if _, err := apiDo("GET", p, nil, &cmp, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, cmp); done || err != nil {
			return err
		}
		fmt.Printf("%s...%s: ahead %s, behind %s\n", args[0], args[1], sv(cmp, "ahead_by"), sv(cmp, "behind_by"))
		printCommits(cmd, asList(cmp, "commits"))
		t := newTable(cmd, "STATUS", "+", "-", "PATH")
		for _, fd := range asList(cmp, "files") {
			t.row(sv(fd, "status"), sv(fd, "additions"), sv(fd, "deletions"), sv(fd, "path"))
		}
		return t.flush()
	})

	// ── commits show / checks ────────────────────────────────────────────────
	cshow := leaf("show SHA", "Show one commit with its files", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/commits/"+seg(args[0]))
		if err != nil {
			return err
		}
		var c rec
		if _, err := apiDo("GET", p, nil, &c, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, c); done || err != nil {
			return err
		}
		fmt.Printf("commit %s\nAuthor: %s\nDate:   %s\n\n%s\n\n", sv(c, "sha"), sv(c, "author"), sv(c, "committed_at"), sv(c, "message"))
		for _, fd := range asList(c, "files") {
			fmt.Printf("%-9s +%s -%s  %s\n", sv(fd, "status"), sv(fd, "additions"), sv(fd, "deletions"), sv(fd, "path"))
		}
		return nil
	})
	cchecks := leaf("checks SHA", "Show the checks of a commit", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/commits/"+seg(args[0])+"/checks")
		if err != nil {
			return err
		}
		var res rec
		if _, err := apiDo("GET", p, nil, &res, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, res); done || err != nil {
			return err
		}
		t := newTable(cmd, "CHECK", "STATE", "SOURCE", "REQUIRED")
		for _, c := range asList(res, "items") {
			t.row(sv(c, "context"), sv(c, "state"), sv(c, "source"), yn(c["required"] == true))
		}
		if err := t.flush(); err != nil {
			return err
		}
		fmt.Printf("Overall: %s\n", sv(res, "state"))
		return nil
	})
	gitCommitsCmd.AddCommand(cshow, cchecks)

	// ── protect ──────────────────────────────────────────────────────────────
	plist := leaf("list", "List branch protection rules", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		items, _, err := listProtections(cmd)
		if err != nil {
			return err
		}
		if done, err := emit(cmd, rec{"items": toAnySlice(items)}); done || err != nil {
			return err
		}
		if quietIDs(cmd, items, "id") {
			return nil
		}
		t := newTable(cmd, "ID", "PATTERN", "PR", "APPROVALS", "CHECKS", "CODEOWNERS", "FORCE-PUSH", "SYNCED")
		for _, r := range items {
			force := "allowed"
			if r["block_force_push"] != false {
				force = "blocked"
			}
			t.row(short8(sv(r, "id")), sv(r, "pattern"), yn(r["require_pull_request"] == true), sv(r, "required_approvals"), sv(r, "required_checks"), yn(r["require_code_owner_review"] == true), force, yn(r["forgejo_synced"] == true))
		}
		return t.flush()
	})
	plist.Flags().BoolP("quiet", "q", false, "Print only IDs")
	pset := leaf("set PATTERN", "Create or update the rule for a branch pattern", cobra.ExactArgs(1), protectSetRun)
	addProtectFlags(pset)
	pset.Example = `  capsule git protect set main --require-pr --approvals 1 --checks "ci / test" --codeowners --no-force-push --block-on-security`
	prem := leaf("remove PATTERN|ID", "Remove a rule", cobra.ExactArgs(1), protectRemoveRun)
	addYes(prem)
	peval := leaf("evaluate", "Which rule applies to a branch", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		branch, _ := cmd.Flags().GetString("branch")
		if branch == "" {
			return usageErr("--branch is required")
		}
		p, err := gp(cmd, "/protections/evaluate"+q("branch", branch))
		if err != nil {
			return err
		}
		var res rec
		if _, err := apiDo("GET", p, nil, &res, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, res); done || err != nil {
			return err
		}
		rule, _ := res["rule"].(map[string]any)
		if rule == nil {
			fmt.Printf("%s -> no rule applies (%s)\n", branch, orDash(sv(res, "reason")))
			return nil
		}
		fmt.Printf("%s -> rule %q (%s)\n", branch, sv(rule, "pattern"), orDash(sv(res, "reason")))
		return nil
	})
	peval.Flags().String("branch", "", "Branch name to evaluate")
	gitCmd.AddCommand(group("protect", "Branch protection rules", plist, pset, prem, peval))

	// ── codeowners ───────────────────────────────────────────────────────────
	codeowners := leaf("codeowners", "Show the parsed CODEOWNERS file and who each rule resolves to", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		ref, _ := cmd.Flags().GetString("ref")
		p, err := gp(cmd, "/codeowners"+q("ref", ref))
		if err != nil {
			return err
		}
		var res rec
		if _, err := apiDo("GET", p, nil, &res, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, res); done || err != nil {
			return err
		}
		fmt.Printf("File: %s\n", orDash(sv(res, "path")))
		t := newTable(cmd, "PATTERN", "OWNERS", "RESOLVED")
		for _, r := range asList(res, "rules") {
			t.row(sv(r, "pattern"), sv(r, "owners"), sv(r, "resolved_users"))
		}
		if err := t.flush(); err != nil {
			return err
		}
		for _, w := range asAnyStrings(res["warnings"]) {
			fmt.Println("warning:", w)
		}
		return nil
	})
	codeowners.Flags().String("ref", "", "Branch, tag or SHA")

	// ── mirror ───────────────────────────────────────────────────────────────
	mshow := leaf("show", "Show the push mirror", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/mirror")
		if err != nil {
			return err
		}
		var m rec
		if _, err := apiDo("GET", p, nil, &m, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, m); done || err != nil {
			return err
		}
		for _, k := range []string{"remote_url", "branches", "interval", "last_sync_at", "last_error"} {
			if v := sv(m, k); v != "" {
				fmt.Printf("%-14s %s\n", k+":", v)
			}
		}
		fmt.Printf("%-14s %s\n", "credential:", yn(m["has_credential"] == true))
		return nil
	})
	mset := leaf("set", "Create or replace the push mirror (credential from stdin, never argv)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		remote, _ := f.GetString("remote-url")
		if remote == "" {
			return usageErr("--remote-url is required")
		}
		interval, _ := f.GetString("interval")
		switch interval {
		case "", "on_push", "hourly", "daily":
		default:
			return usageErr("--interval must be on_push, hourly or daily")
		}
		body := rec{"remote_url": remote}
		if interval != "" {
			body["interval"] = interval
		}
		if b := csvFlag(cmd, "branches"); len(b) > 0 {
			body["branches"] = b
		}
		if ts, _ := f.GetBool("credential-stdin"); ts {
			c, err := readStdinSecret("mirror credential")
			if err != nil {
				return usageErr("%v", err)
			}
			body["credential"] = c
		}
		p, err := gp(cmd, "/mirror")
		if err != nil {
			return err
		}
		var m rec
		if _, err := apiDo("PUT", p, body, &m, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, m); done || err != nil {
			return err
		}
		fmt.Printf("Mirror to %s saved.\n", remote)
		return nil
	})
	mset.Flags().String("remote-url", "", "Remote repository URL")
	mset.Flags().String("interval", "", "on_push, hourly or daily")
	mset.Flags().StringSlice("branches", nil, "Branches to mirror (default: all)")
	mset.Flags().Bool("credential-stdin", false, "Read the remote credential from stdin")
	mrm := leaf("remove", "Remove the push mirror", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		if err := confirmOr(cmd, "Remove the push mirror?"); err != nil {
			return err
		}
		p, err := gp(cmd, "/mirror")
		if err != nil {
			return err
		}
		if _, err := apiDo("DELETE", p, nil, nil, nil); err != nil {
			return err
		}
		fmt.Println("Mirror removed.")
		return nil
	})
	addYes(mrm)
	msync := leaf("sync", "Push to the mirror now", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/mirror/sync")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{}, &out, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Println("Mirror sync started.")
		return nil
	})
	addIdem(msync)
	gitCmd.AddCommand(group("mirror", "Push mirror to another Git server", mshow, mset, mrm, msync))

	// ── import github ────────────────────────────────────────────────────────
	imp := orgLeaf("github", "Import repositories from GitHub (token from stdin)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		repos := csvFlag(cmd, "repos")
		if len(repos) == 0 {
			return usageErr("--repos OWNER/NAME[,OWNER/NAME...] is required")
		}
		if ts, _ := f.GetBool("token-stdin"); !ts {
			return usageErr("--token-stdin is required: the GitHub token is read from stdin, never from argv")
		}
		tok, err := readStdinSecret("GitHub token")
		if err != nil {
			return usageErr("%v", err)
		}
		body := rec{"source": "github", "token": tok, "repos": repos}
		if v, _ := f.GetString("target-org"); v != "" {
			body["target_org_id"] = v
		}
		if f.Changed("create-projects") {
			v, _ := f.GetBool("create-projects")
			body["create_projects"] = v
		}
		if inc := csvFlag(cmd, "include"); len(inc) > 0 {
			body["include"] = inc
		}
		p, err := op(cmd, "/git/imports")
		if err != nil {
			return err
		}
		var job rec
		if _, err := apiDo("POST", p, body, &job, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if done, err := emit(cmd, job); done || err != nil {
			return err
		}
		fmt.Printf("Import %s %s. Follow it with: capsule git import status %s\n", short8(sv(job, "id")), sv(job, "status"), short8(sv(job, "id")))
		return nil
	})
	imp.Flags().StringSlice("repos", nil, "GitHub repositories OWNER/NAME (comma separated)")
	imp.Flags().Bool("token-stdin", false, "Read the GitHub token from stdin")
	imp.Flags().String("target-org", "", "Target org id (default: the current org)")
	imp.Flags().Bool("create-projects", true, "Create a Capsule project per repository")
	imp.Flags().StringSlice("include", nil, "issues,pull_requests,wiki,releases,labels")
	addIdem(imp)
	istatus := orgLeaf("status ID", "Show an import job", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, err := op(cmd, "/git/imports/"+seg(args[0]))
		if err != nil {
			return err
		}
		var job rec
		if _, err := apiDo("GET", p, nil, &job, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, job); done || err != nil {
			return err
		}
		fmt.Printf("Import %s: %s\n", short8(sv(job, "id")), sv(job, "status"))
		t := newTable(cmd, "REPOSITORY", "STATUS", "PROJECT", "ERROR")
		for _, r := range asList(job, "repos") {
			t.row(sv(r, "name"), sv(r, "status"), short8(sv(r, "project_id")), sv(r, "error"))
		}
		return t.flush()
	})
	gitCmd.AddCommand(group("import", "Import repositories from another provider", imp, istatus))

	// ── status / activity / insights / reconcile ─────────────────────────────
	gstatus := userLeaf("status", "What the Git engine supports on this server (version, runners, features)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		var st rec
		if _, err := apiDo("GET", "/api/v1/git/status", nil, &st, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, st); done || err != nil {
			return err
		}
		fmt.Printf("ready:    %s\nforgejo:  %s\n", yn(st["ready"] == true), orDash(sv(st, "forgejo_version")))
		if r, ok := st["runners"].(map[string]any); ok {
			fmt.Printf("runners:  %s online (%s)\n", sv(r, "online"), sv(r, "labels"))
		}
		if f := sv(st, "features"); f != "" {
			fmt.Printf("features: %s\n", f)
		}
		return nil
	})
	activity := leaf("activity", "Recent activity of the repository", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/activity")
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
		if quietIDs(cmd, items) {
			return nil
		}
		t := newTable(cmd, "WHEN", "TYPE", "ACTOR", "SUMMARY")
		for _, a := range items {
			t.row(age(sv(a, "created_at")), sv(a, "type"), sv(a, "actor"), truncate(sv(a, "summary"), 70))
		}
		return t.flush()
	})
	addList(activity)
	insights := leaf("insights", "Commits, PRs, review time, deploys and contributors", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		since, _ := cmd.Flags().GetString("since")
		until, _ := cmd.Flags().GetString("until")
		if since != "" {
			if d, err := parseDays(since); err == nil {
				since = nowUTC().Add(-d).Format("2006-01-02T15:04:05Z")
			}
		}
		p, err := gp(cmd, "/insights"+q("since", since, "until", until))
		if err != nil {
			return err
		}
		var in rec
		if _, err := apiDo("GET", p, nil, &in, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, in); done || err != nil {
			return err
		}
		for _, k := range []string{"commits", "prs_opened", "prs_merged", "median_time_to_first_review_s", "median_time_to_merge_s", "deploys", "change_failure_rate"} {
			fmt.Printf("%-32s %s\n", k, orDash(sv(in, k)))
		}
		t := newTable(cmd, "CONTRIBUTOR", "COMMITS", "+", "-")
		for _, c := range asList(in, "contributors") {
			t.row(sv(c, "person"), sv(c, "commits"), sv(c, "additions"), sv(c, "deletions"))
		}
		return t.flush()
	})
	insights.Flags().String("since", "", "Start: RFC 3339 time or a relative age such as 30d")
	insights.Flags().String("until", "", "End: RFC 3339 time")
	reconcile := userLeaf("reconcile", "Re-sync Git state with the engine (platform administrators)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		if err := confirmOr(cmd, "Run the platform Git reconciliation?"); err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", "/api/v1/git/admin/reconcile", rec{}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Println("Reconciliation started.")
		return nil
	})
	addYes(reconcile)

	// ── repo view/create/import/settings under `git repo` ────────────────────
	rview := leaf("view", "Show the repository (provisioning state, default branch, clone URLs)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/repo")
		if err != nil {
			return err
		}
		var repo rec
		if _, err := apiDo("GET", p, nil, &repo, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, repo); done || err != nil {
			return err
		}
		fmt.Printf("provisioning:   %s\n", sv(repo, "provision_status"))
		if e := sv(repo, "last_error"); e != "" {
			fmt.Printf("last error:     %s\n", e)
		}
		for _, k := range []string{"default_branch", "clone_https", "clone_ssh", "forgejo_url", "size_kb", "allowed_merge_styles"} {
			if v := sv(repo, k); v != "" {
				fmt.Printf("%-15s %s\n", strings.ReplaceAll(k, "_", " ")+":", v)
			}
		}
		if c, ok := repo["clone"].(map[string]any); ok {
			if cc, ok := c["capsule"].(map[string]any); ok && sv(cc, "command") != "" {
				fmt.Printf("clone (Capsule): %s\n", sv(cc, "command"))
			}
		}
		return nil
	})
	rcreate := leaf("create", "Create the repository in Capsule's Git", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		body := rec{}
		if u, _ := cmd.Flags().GetString("import-url"); u != "" {
			body["import_url"] = u
		}
		if b, _ := cmd.Flags().GetString("default-branch"); b != "" {
			body["default_branch"] = b
		}
		p, err := gp(cmd, "/repo")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, body, &out, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Repository %s.\n", orDash(sv(out, "provision_status")))
		return nil
	})
	rcreate.Flags().String("import-url", "", "Import the content from this URL")
	rcreate.Flags().String("default-branch", "", "Default branch name")
	addIdem(rcreate)
	rimport := leaf("import URL", "Create the repository importing another URL", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, err := gp(cmd, "/repo")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{"import_url": args[0]}, &out, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Import of %s started (%s).\n", redactURL(args[0]), orDash(sv(out, "provision_status")))
		return nil
	})
	addIdem(rimport)
	rsettings := leaf("settings", "Change repository settings", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		body := rec{}
		if f.Changed("default-branch") {
			body["default_branch"], _ = f.GetString("default-branch")
		}
		if f.Changed("delete-branch-on-merge") {
			body["delete_branch_on_merge"], _ = f.GetBool("delete-branch-on-merge")
		}
		if f.Changed("archived") {
			body["archived"], _ = f.GetBool("archived")
		}
		if f.Changed("merge-styles") {
			body["allowed_merge_styles"] = csvFlag(cmd, "merge-styles")
		}
		if len(body) == 0 {
			return usageErr("nothing to change: pass --default-branch, --delete-branch-on-merge, --archived or --merge-styles")
		}
		if a, ok := body["archived"].(bool); ok && a {
			if err := confirmOr(cmd, "Archive the repository (read-only)?"); err != nil {
				return err
			}
		}
		p, err := gp(cmd, "/repo")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("PATCH", p, body, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Println("Repository settings updated.")
		return nil
	})
	rsettings.Flags().String("default-branch", "", "Default branch")
	rsettings.Flags().Bool("delete-branch-on-merge", false, "Delete head branches after merging")
	rsettings.Flags().Bool("archived", false, "Archive (true) or unarchive (false)")
	rsettings.Flags().StringSlice("merge-styles", nil, "Allowed merge styles: merge,squash,rebase,fast-forward")
	addYes(rsettings)
	gitRepoCmd.AddCommand(rview, rcreate, rimport, rsettings)

	gitCmd.AddCommand(tree, cat, compare, codeowners, gstatus, activity, insights, reconcile)

	// ── `capsule repo`: gh-style aliases (CLI.md 3.2) ────────────────────────
	rls := cloneCmd(tree, "ls [PATH]")
	rcat := cloneCmd(cat, "cat PATH")
	rv := cloneCmd(rview, "view")
	rc := cloneCmd(rcreate, "create")
	rclone := cloneCmd(gitCloneCmd, "clone [ORG/REPO] [DIR] [-- GIT-ARGS...]")
	rclone.Args = cobra.ArbitraryArgs
	rclone.Short = "Clone ORG/REPO with the CLI session, or the linked project's repository when no argument is given"
	cloneWithSession := gitCloneCmd.RunE
	rclone.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return cloneWithSession(cmd, args)
		}
		g, err := fetchGit(cmd)
		if err != nil {
			return err
		}
		u := str(g, "clone_url_https")
		if u == "" {
			return usageErr("the linked project has no HTTPS clone URL yet (see `capsule git remote`)")
		}
		return runGitSteps([]gitStep{{Args: []string{"clone", u}}})
	}
	addNetFlags(rclone)
	rootCmd.AddCommand(group("repo", "Repository shortcuts (aliases of `capsule git ...`)", rls, rcat, rv, rc, rclone))
}

func toAnySlice(rs []rec) []any {
	o := make([]any, len(rs))
	for i, r := range rs {
		o[i] = r
	}
	return o
}

func asAnyStrings(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, e := range a {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
