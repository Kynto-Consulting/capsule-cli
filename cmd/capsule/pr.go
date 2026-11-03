package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// ── capsule pr ───────────────────────────────────────────────────────────────
// CLI.md 3.2. operationIds: pullsList pullGet reviewsList commentsList
// pullCreate pullUpdate pullReady pullFiles pullCommits pullChecks reviewCreate
// reviewDismiss reviewersRequest commentCreate pullUpdateBranch pullMerge
// autoMergeEnable autoMergeDisable.

// pullURL builds /git/pulls/{n}+suffix for a PR reference argument.
func pullURL(cmd *cobra.Command, ref, suffix string) (string, string, error) {
	n, err := prNumber(ref)
	if err != nil {
		return "", "", err
	}
	p, err := gp(cmd, "/pulls/"+n+suffix)
	return p, n, err
}

func getPull(cmd *cobra.Command, ref string) (rec, string, error) {
	p, n, err := pullURL(cmd, ref, "")
	if err != nil {
		return nil, "", err
	}
	var pr rec
	if _, err := apiDo("GET", p, nil, &pr, nil); err != nil {
		return nil, "", err
	}
	return pr, n, nil
}

func blockers(pr rec) []string {
	var out []string
	mg, _ := pr["mergeable"].(map[string]any)
	if bl, ok := mg["blockers"].([]any); ok {
		for _, b := range bl {
			if bm, ok := b.(map[string]any); ok {
				out = append(out, fmt.Sprintf("%s (%s)", sv(bm, "code"), sv(bm, "message")))
			}
		}
	}
	return out
}

var prCmd = group("pr", "Pull requests: list, view, create, review, check and merge")

func init() {
	prCmd.Aliases = []string{"pulls"}

	list := leaf("list", "List pull requests", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		state, _ := f.GetString("state")
		switch state {
		case "open", "closed", "merged", "all":
		default:
			return usageErr("invalid --state %q (open, closed, merged, all)", state)
		}
		author, _ := f.GetString("author")
		author, err := resolveMe(author)
		if err != nil {
			return err
		}
		base, _ := f.GetString("base")
		p, err := gp(cmd, "/pulls"+q("state", state, "author", author, "base", base))
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
		if quietIDs(cmd, items, "number") {
			return nil
		}
		t := newTable(cmd, "PR", "STATE", "AUTHOR", "BRANCH", "CHECKS", "TITLE")
		for _, pr := range items {
			state := sv(pr, "state")
			if b, _ := pr["draft"].(bool); b {
				state += " (draft)"
			}
			t.row("#"+sv(pr, "number"), state, sv(pr, "author"), sv(pr, "head")+" -> "+sv(pr, "base"), sv(pr, "checks_state"), truncate(sv(pr, "title"), 60))
		}
		return t.flush()
	})
	list.Aliases = []string{"ls"}
	list.Flags().String("state", "open", "open, closed, merged or all")
	list.Flags().String("author", "", "Filter by author login (@me for yourself)")
	list.Flags().String("base", "", "Filter by base branch")
	addList(list)

	view := leaf("view N", "Show a pull request with checks, reviews and blockers", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		pr, n, err := getPull(cmd, args[0])
		if err != nil {
			return err
		}
		if web, _ := cmd.Flags().GetBool("web"); web {
			return webOpen(sv(pr, "html_url"))
		}
		var reviews rec
		rp, _, _ := pullURL(cmd, n, "/reviews")
		if _, err := apiDo("GET", rp, nil, &reviews, nil); err != nil {
			return err
		}
		var comments any
		withComments, _ := cmd.Flags().GetBool("comments")
		if withComments {
			cp, _, _ := pullURL(cmd, n, "/comments")
			if _, err := apiDo("GET", cp, nil, &comments, nil); err != nil {
				return err
			}
		}
		if jsonMode(cmd) {
			out := rec{"pull": pr, "reviews": reviews}
			if withComments {
				out["comments"] = comments
			}
			_, err := emit(cmd, out)
			return err
		}
		state := sv(pr, "state")
		if b, _ := pr["draft"].(bool); b {
			state += ", draft"
		}
		fmt.Printf("#%s %s   [%s]  %s -> %s\n", n, sv(pr, "title"), state, sv(pr, "head"), sv(pr, "base"))
		fmt.Printf("Author: %s\n", sv(pr, "author"))
		if cs := sv(pr, "checks_state"); cs != "" {
			fmt.Printf("Checks: %s\n", cs)
		}
		req := sv(reviews, "required_approvals")
		fmt.Printf("Reviews: %s/%s approved", sv(reviews, "approvals"), orDash(req))
		if cp := sv(reviews, "code_owner_pending"); cp != "" {
			fmt.Printf(" · CODEOWNERS pending: %s", cp)
		}
		fmt.Println()
		for _, r := range asList(reviews, "items") {
			fmt.Printf("  %-18s %s%s\n", sv(r, "state"), sv(r, "author"), map[bool]string{true: " (stale)", false: ""}[r["stale"] == true])
		}
		if bl := blockers(pr); len(bl) > 0 {
			fmt.Println("Blockers:")
			for _, b := range bl {
				fmt.Println("  " + b)
			}
		} else if mg, ok := pr["mergeable"].(map[string]any); ok && mg["can_merge"] == true {
			fmt.Println("Mergeable: yes")
		}
		if u := sv(pr, "preview_url"); u != "" {
			fmt.Println("Preview:", u)
		}
		if am, ok := pr["auto_merge"].(map[string]any); ok && am != nil {
			fmt.Printf("Auto-merge: on (%s)\n", sv(am, "style"))
		}
		if b := strings.TrimSpace(sv(pr, "body")); b != "" {
			fmt.Printf("\n%s\n", b)
		}
		if withComments {
			cs, _ := pageOf(comments)
			for _, c := range cs {
				fmt.Printf("\n%s (%s):\n%s\n", sv(c, "author"), age(sv(c, "created_at")), sv(c, "body"))
			}
		}
		return nil
	})
	view.Flags().Bool("comments", false, "Include the comments")
	view.Flags().Bool("web", false, "Print the web URL instead")

	create := leaf("create", "Open a pull request", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		title, _ := f.GetString("title")
		if title == "" {
			return usageErr("--title is required")
		}
		body, err := bodyFromFlags(cmd)
		if err != nil {
			return err
		}
		head, _ := f.GetString("head")
		if head == "" {
			head = firstEnv("GITHUB_HEAD_REF", "CAPSULE_GIT_BRANCH")
		}
		if head == "" {
			head = gitBranch()
		}
		if head == "" || head == "HEAD" {
			return usageErr("--head is required (could not detect the current branch)")
		}
		base, _ := f.GetString("base")
		if base == "" {
			rp, err := gp(cmd, "/repo")
			if err != nil {
				return err
			}
			var repo rec
			if _, err := apiDo("GET", rp, nil, &repo, nil); err != nil {
				return err
			}
			base = sv(repo, "default_branch")
			if base == "" {
				return usageErr("--base is required (the repository reports no default branch)")
			}
		}
		payload := rec{"title": title, "head": head, "base": base}
		if body != "" {
			payload["body"] = body
		}
		if d, _ := f.GetBool("draft"); d {
			payload["draft"] = true
		}
		if rv := csvFlag(cmd, "reviewer"); len(rv) > 0 {
			payload["reviewers"] = rv
		}
		p, err := gp(cmd, "/pulls")
		if err != nil {
			return err
		}
		var pr rec
		if _, err := apiDo("POST", p, payload, &pr, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, pr); done || err != nil {
			return err
		}
		fmt.Printf("Pull request #%s opened: %s -> %s\n", sv(pr, "number"), head, base)
		if u := sv(pr, "html_url"); u != "" {
			fmt.Println(u)
		}
		return nil
	})
	create.Flags().String("title", "", "Title")
	create.Flags().StringP("body", "b", "", "Description")
	create.Flags().String("body-file", "", "Read the description from a file (- for stdin)")
	create.Flags().String("base", "", "Base branch (default: the repository default branch)")
	create.Flags().String("head", "", "Head branch (default: current branch)")
	create.Flags().Bool("draft", false, "Open as draft")
	create.Flags().StringSlice("reviewer", nil, "Reviewer logins (comma separated)")

	edit := leaf("edit N", "Edit title, description or base branch", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		payload := rec{}
		if f.Changed("title") {
			payload["title"], _ = f.GetString("title")
		}
		if f.Changed("body") || f.Changed("body-file") {
			b, err := bodyFromFlags(cmd)
			if err != nil {
				return err
			}
			payload["body"] = b
		}
		if f.Changed("base") {
			payload["base"], _ = f.GetString("base")
		}
		if len(payload) == 0 {
			return usageErr("nothing to change: pass --title, --body, --body-file or --base")
		}
		return patchPull(cmd, args[0], payload, "updated")
	})
	edit.Flags().String("title", "", "New title")
	edit.Flags().StringP("body", "b", "", "New description")
	edit.Flags().String("body-file", "", "Read the description from a file (- for stdin)")
	edit.Flags().String("base", "", "New base branch")

	closeC := leaf("close N", "Close a pull request without merging", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return patchPull(cmd, args[0], rec{"state": "closed"}, "closed")
	})
	reopen := leaf("reopen N", "Reopen a closed pull request", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return patchPull(cmd, args[0], rec{"state": "open"}, "reopened")
	})

	ready := leaf("ready N", "Mark a draft pull request as ready for review", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, n, err := pullURL(cmd, args[0], "/ready")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Pull request #%s is ready for review.\n", n)
		return nil
	})

	diff := leaf("diff N", "Print the diff of a pull request", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		if web, _ := cmd.Flags().GetBool("web"); web {
			pr, _, err := getPull(cmd, args[0])
			if err != nil {
				return err
			}
			return webOpen(sv(pr, "html_url"))
		}
		p, _, err := pullURL(cmd, args[0], "/files")
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
		for _, fd := range items {
			fmt.Printf("diff --capsule a/%s b/%s\n", sv(fd, "old_path", "path"), sv(fd, "path"))
			if patch := sv(fd, "patch"); patch != "" {
				fmt.Println(strings.TrimRight(patch, "\n"))
			}
			if b, _ := fd["truncated"].(bool); b {
				fmt.Println("... (diff truncated by the server; use --web for the full diff)")
			}
		}
		return nil
	})
	diff.Flags().Bool("web", false, "Print the web URL of the diff instead")
	addList(diff)

	files := leaf("files N", "List the files changed by a pull request", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, _, err := pullURL(cmd, args[0], "/files")
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
		t := newTable(cmd, "STATUS", "+", "-", "PATH")
		for _, fd := range items {
			t.row(sv(fd, "status"), sv(fd, "additions"), sv(fd, "deletions"), sv(fd, "path"))
		}
		return t.flush()
	})
	addList(files)

	commits := leaf("commits N", "List the commits of a pull request", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, _, err := pullURL(cmd, args[0], "/commits")
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
		if quietIDs(cmd, items, "sha") {
			return nil
		}
		printCommits(cmd, items)
		return nil
	})
	addList(commits)

	checks := leaf("checks N", "Show the checks of a pull request", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, _, err := pullURL(cmd, args[0], "/checks")
		if err != nil {
			return err
		}
		f := cmd.Flags()
		required, _ := f.GetBool("required")
		watch, _ := f.GetBool("watch")
		exitStatus, _ := f.GetBool("exit-status")
		var res rec
		show := func() error {
			res = rec{}
			if _, err := apiDo("GET", p, nil, &res, nil); err != nil {
				return err
			}
			return nil
		}
		if err := show(); err != nil {
			return err
		}
		if watch {
			timeout := waitDuration(cmd)
			if timeout == 0 {
				timeout = 30 * 60 * 1e9
			}
			err := pollUntil(timeout, "checks", func() (bool, string, error) {
				if err := show(); err != nil {
					return false, "", err
				}
				st := sv(res, "state")
				return st != "pending" && st != "", "state " + st, nil
			})
			if err != nil {
				return err
			}
		}
		if done, err := emit(cmd, res); done || err != nil {
			if err != nil {
				return err
			}
		} else {
			t := newTable(cmd, "CHECK", "STATE", "SOURCE", "REQUIRED")
			for _, c := range asList(res, "items") {
				if required && c["required"] != true {
					continue
				}
				t.row(sv(c, "context"), sv(c, "state"), sv(c, "source"), yn(c["required"] == true))
			}
			if err := t.flush(); err != nil {
				return err
			}
			fmt.Printf("Overall: %s\n", sv(res, "state"))
		}
		if exitStatus && sv(res, "state") == "failure" {
			return codedErr(exitTimeout, "checks failed")
		}
		return nil
	})
	checks.Flags().Bool("watch", false, "Poll until no check is pending")
	checks.Flags().Bool("required", false, "Show only required checks")
	checks.Flags().Bool("exit-status", false, "Exit 6 when the overall state is failure")
	addWait(checks, "the checks (with --watch)")

	review := leaf("review N", "Approve, request changes or comment on a pull request", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		var events []string
		for flag, ev := range map[string]string{"approve": "approve", "request-changes": "request_changes", "comment": "comment"} {
			if b, _ := f.GetBool(flag); b {
				events = append(events, ev)
			}
		}
		if len(events) != 1 {
			return usageErr("choose exactly one of --approve, --request-changes, --comment")
		}
		body, _ := f.GetString("body")
		payload := rec{"event": events[0]}
		if body != "" {
			payload["body"] = body
		}
		if events[0] != "approve" && body == "" && !f.Changed("file") {
			return usageErr("--request-changes and --comment need -b TEXT or --file F:L:TEXT")
		}
		files, _ := f.GetStringArray("file")
		if len(files) > 0 {
			var inl []rec
			for _, spec := range files {
				parts := strings.SplitN(spec, ":", 3)
				if len(parts) != 3 {
					return usageErr("--file must be PATH:LINE:TEXT, got %q", spec)
				}
				line, err := strconv.Atoi(parts[1])
				if err != nil || line < 1 {
					return usageErr("--file line must be a positive number in %q", spec)
				}
				inl = append(inl, rec{"path": parts[0], "line": line, "body": parts[2]})
			}
			payload["comments"] = inl
		}
		if sha, _ := f.GetString("commit"); sha != "" {
			payload["commit_sha"] = sha
		}
		p, n, err := pullURL(cmd, args[0], "/reviews")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, payload, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Review submitted on #%s: %s\n", n, events[0])
		return nil
	})
	review.Flags().Bool("approve", false, "Approve")
	review.Flags().Bool("request-changes", false, "Request changes")
	review.Flags().Bool("comment", false, "Leave a comment review")
	review.Flags().StringP("body", "b", "", "Review text")
	review.Flags().StringArray("file", nil, "Inline comment PATH:LINE:TEXT (repeatable)")
	review.Flags().String("commit", "", "Review a specific commit SHA")

	dismiss := leaf("dismiss-review N REVIEW_ID", "Dismiss a review (repository admin)", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			return usageErr("--reason is required")
		}
		rl, _, err := pullURL(cmd, args[0], "/reviews")
		if err != nil {
			return err
		}
		id, err := resolveShort(rl, args[1], "review")
		if err != nil {
			return err
		}
		p, n, _ := pullURL(cmd, args[0], "/reviews/"+seg(id)+"/dismiss")
		var out rec
		if _, err := apiDo("POST", p, rec{"reason": reason}, &out, nil); err != nil {
			return err
		}
		fmt.Printf("Review %s on #%s dismissed.\n", short8(id), n)
		return nil
	})
	dismiss.Flags().String("reason", "", "Why the review is dismissed")

	reqReview := leaf("request-review N", "Request reviews from users or teams", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		users, teams := csvFlag(cmd, "user"), csvFlag(cmd, "team")
		if len(users)+len(teams) == 0 {
			return usageErr("pass --user and/or --team")
		}
		payload := rec{}
		if len(users) > 0 {
			payload["users"] = users
		}
		if len(teams) > 0 {
			payload["teams"] = teams
		}
		p, n, err := pullURL(cmd, args[0], "/reviewers")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, payload, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Review requested on #%s.\n", n)
		return nil
	})
	reqReview.Flags().StringSlice("user", nil, "User logins (comma separated)")
	reqReview.Flags().StringSlice("team", nil, "Team names (comma separated)")

	comment := leaf("comment N", "Add a comment to a pull request", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		body, err := bodyFromFlags(cmd)
		if err != nil {
			return err
		}
		if body == "" {
			return usageErr("-b TEXT (or --body-file) is required")
		}
		p, n, err := pullURL(cmd, args[0], "/comments")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{"body": body}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Comment added to #%s.\n", n)
		return nil
	})
	comment.Flags().StringP("body", "b", "", "Comment text")
	comment.Flags().String("body-file", "", "Read the comment from a file (- for stdin)")

	updateBranch := leaf("update-branch N", "Merge the base branch into the pull request branch", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, n, err := pullURL(cmd, args[0], "/update-branch")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{}, &out, nil); err != nil {
			return err
		}
		wait := waitDuration(cmd)
		if wait > 0 {
			err := pollUntil(wait, "branch update", func() (bool, string, error) {
				pr, _, err := getPull(cmd, n)
				if err != nil {
					return false, "", err
				}
				for _, b := range blockers(pr) {
					if strings.HasPrefix(b, "BRANCH_OUT_OF_DATE") {
						return false, "still out of date", nil
					}
				}
				return true, "up to date", nil
			})
			if err != nil {
				return err
			}
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Branch of #%s updated.\n", n)
		return nil
	})
	addWait(updateBranch, "the branch update")

	merge := leaf("merge N", "Merge a pull request (or schedule auto-merge)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		n, err := prNumber(args[0])
		if err != nil {
			return err
		}
		if dis, _ := f.GetBool("disable-auto"); dis {
			p, _, _ := pullURL(cmd, n, "/auto-merge")
			if _, err := apiDo("DELETE", p, nil, nil, nil); err != nil {
				return err
			}
			fmt.Printf("Auto-merge cancelled on #%s.\n", n)
			return nil
		}
		style := "merge"
		var chosen []string
		for flag, st := range map[string]string{"squash": "squash", "rebase": "rebase", "merge": "merge", "ff": "fast-forward"} {
			if b, _ := f.GetBool(flag); b {
				chosen = append(chosen, st)
			}
		}
		if len(chosen) > 1 {
			return usageErr("choose only one of --squash, --rebase, --merge, --ff")
		}
		if len(chosen) == 1 {
			style = chosen[0]
		}
		payload := rec{"style": style}
		if d, _ := f.GetBool("delete-branch"); d {
			payload["delete_branch"] = true
		}
		if s, _ := f.GetString("head-sha"); s != "" {
			payload["head_sha"] = s
		}
		if s, _ := f.GetString("title"); s != "" {
			payload["title"] = s
		}
		if s, _ := f.GetString("message"); s != "" {
			payload["message"] = s
		}
		if auto, _ := f.GetBool("auto"); auto {
			p, _, _ := pullURL(cmd, n, "/auto-merge")
			var out rec
			if _, err := apiDo("PUT", p, payload, &out, nil); err != nil {
				return err
			}
			if done, err := emit(cmd, out); done || err != nil {
				return err
			}
			fmt.Printf("Auto-merge scheduled: #%s will be merged (%s) when the checks pass and the rules are met.\n", n, style)
			return nil
		}
		p, _, _ := pullURL(cmd, n, "/merge")
		var out rec
		if _, err := apiDo("POST", p, payload, &out, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		switch {
		case out["queued"] == true:
			fmt.Printf("#%s queued for merge (%s).\n", n, style)
		default:
			fmt.Printf("#%s merged (%s) %s\n", n, style, shortSHA(sv(out, "sha")))
		}
		return nil
	})
	merge.Flags().Bool("squash", false, "Squash and merge")
	merge.Flags().Bool("rebase", false, "Rebase and merge")
	merge.Flags().Bool("merge", false, "Create a merge commit (default)")
	merge.Flags().Bool("ff", false, "Fast-forward only")
	merge.Flags().Bool("delete-branch", false, "Delete the head branch after merging")
	merge.Flags().Bool("auto", false, "Merge automatically when checks pass and rules are met")
	merge.Flags().Bool("disable-auto", false, "Cancel a scheduled auto-merge")
	merge.Flags().String("head-sha", "", "Only merge if the head is still this SHA (409 otherwise)")
	merge.Flags().String("title", "", "Merge commit title")
	merge.Flags().String("message", "", "Merge commit message")
	addIdem(merge)

	status := leaf("status", "Your pull requests and the ones waiting for your review", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		me, err := meLogin()
		if err != nil {
			return err
		}
		p, err := gp(cmd, "/pulls"+q("state", "open"))
		if err != nil {
			return err
		}
		items, raw, err := fetchList(cmd, p)
		if err != nil {
			return err
		}
		var mine, review []rec
		for _, pr := range items {
			if strings.EqualFold(sv(pr, "author"), me) {
				mine = append(mine, pr)
				continue
			}
			for _, r := range asList(rec{"x": pr["reviewers"]}, "x") {
				if strings.EqualFold(sv(r, "login"), me) {
					review = append(review, pr)
					break
				}
			}
		}
		if jsonMode(cmd) {
			toAny := func(rs []rec) []any {
				o := make([]any, len(rs))
				for i, r := range rs {
					o[i] = r
				}
				return o
			}
			_ = raw
			_, err := emit(cmd, rec{"created_by_you": toAny(mine), "review_requested": toAny(review)})
			return err
		}
		section := func(title string, rs []rec) {
			fmt.Println(title)
			if len(rs) == 0 {
				fmt.Println("  (none)")
			}
			for _, pr := range rs {
				fmt.Printf("  #%-5s %-12s %s\n", sv(pr, "number"), sv(pr, "checks_state"), truncate(sv(pr, "title"), 70))
			}
		}
		section("Created by you", mine)
		section("Review requested from you", review)
		return nil
	})
	addList(status)

	co := leaf("co N", "Check out a pull request locally (git fetch origin pull/N/head)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		n, err := prNumber(args[0])
		if err != nil {
			return err
		}
		remote, _ := cmd.Flags().GetString("remote")
		branch := "pr-" + n
		return runGitSteps([]gitStep{
			{Args: []string{"fetch", remote, "pull/" + n + "/head:" + branch}},
			{Args: []string{"checkout", branch}},
		})
	})
	co.Flags().String("remote", "origin", "Git remote to fetch from")

	prCmd.AddCommand(list, view, create, edit, closeC, reopen, ready, diff, files, commits, checks, review, dismiss, reqReview, comment, updateBranch, merge, status, co)
	rootCmd.AddCommand(prCmd)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func patchPull(cmd *cobra.Command, ref string, payload rec, verb string) error {
	p, n, err := pullURL(cmd, ref, "")
	if err != nil {
		return err
	}
	var pr rec
	if _, err := apiDo("PATCH", p, payload, &pr, nil); err != nil {
		return err
	}
	if done, err := emit(cmd, pr); done || err != nil {
		return err
	}
	fmt.Printf("Pull request #%s %s.\n", n, verb)
	return nil
}

// bodyFromFlags reads --body or --body-file (- = stdin).
func bodyFromFlags(cmd *cobra.Command) (string, error) {
	f := cmd.Flags()
	b, _ := f.GetString("body")
	file, _ := f.GetString("body-file")
	if b != "" && file != "" {
		return "", usageErr("use either --body or --body-file, not both")
	}
	if file == "" {
		return b, nil
	}
	if file == "-" {
		raw, err := readAllStdin()
		return strings.TrimRight(raw, "\n"), err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", usageErr("reading --body-file: %v", err)
	}
	return strings.TrimRight(string(raw), "\n"), nil
}

func printCommits(cmd *cobra.Command, items []rec) {
	t := newTable(cmd, "SHA", "AUTHOR", "DATE", "MESSAGE")
	for _, c := range items {
		msg := strings.SplitN(sv(c, "message"), "\n", 2)[0]
		t.row(shortSHA(sv(c, "sha")), sv(c, "author"), age(sv(c, "committed_at")), truncate(msg, 70))
	}
	_ = t.flush()
}
