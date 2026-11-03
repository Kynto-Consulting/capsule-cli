package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// ── capsule environments ─────────────────────────────────────────────────────
// CLI.md 3.6. operationIds: envsList envGet envCreate envPut envDelete
// envDeployments. (`capsule env` stays the project's environment variables.)

var envNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

var dayIndex = map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}
var dayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// parseWindow parses "mon-fri 09:00-18:00 America/Lima" or "sat,sun 00:00-23:59 UTC".
func parseWindow(s string) (rec, error) {
	parts := strings.Fields(s)
	if len(parts) < 2 || len(parts) > 3 {
		return nil, usageErr("--window must be \"DAYS HH:MM-HH:MM [TIMEZONE]\", e.g. \"mon-fri 09:00-18:00 America/Lima\"; got %q", s)
	}
	var days []string
	for _, tok := range strings.Split(strings.ToLower(parts[0]), ",") {
		if a, b, ok := strings.Cut(tok, "-"); ok {
			ia, oka := dayIndex[a]
			ib, okb := dayIndex[b]
			if !oka || !okb || ia > ib {
				return nil, usageErr("bad day range %q in --window", tok)
			}
			for i := ia; i <= ib; i++ {
				days = append(days, dayNames[i])
			}
			continue
		}
		if _, ok := dayIndex[tok]; !ok {
			return nil, usageErr("bad day %q in --window (mon..sun)", tok)
		}
		days = append(days, tok)
	}
	from, to, ok := strings.Cut(parts[1], "-")
	if !ok || !hhmmRe.MatchString(from) || !hhmmRe.MatchString(to) {
		return nil, usageErr("bad time range %q in --window (HH:MM-HH:MM)", parts[1])
	}
	w := rec{"days": days, "from": from, "to": to}
	if len(parts) == 3 {
		w["tz"] = parts[2]
	} else {
		w["tz"] = "UTC"
	}
	return w, nil
}

func parseReviewers(cmd *cobra.Command, specs []string) ([]rec, error) {
	var out []rec
	for _, sp := range specs {
		kind, val, ok := strings.Cut(sp, ":")
		if !ok || val == "" {
			return nil, usageErr("--reviewers entries are user:EMAIL|ID or role:admin|deployer; got %q", sp)
		}
		switch kind {
		case "user":
			id, err := resolveUser(cmd, val)
			if err != nil {
				return nil, err
			}
			out = append(out, rec{"type": "user", "user_id": id})
		case "role":
			if val != "admin" && val != "deployer" {
				return nil, usageErr("role reviewers are admin or deployer; got %q", val)
			}
			out = append(out, rec{"type": "role", "role": val})
		default:
			return nil, usageErr("--reviewers entries are user:... or role:...; got %q", sp)
		}
	}
	return out, nil
}

func addEnvFlags(c *cobra.Command) {
	f := c.Flags()
	f.String("kind", "", "staging, production or other")
	f.String("target", "", "Project that receives the deployments (id or slug)")
	f.StringSlice("branches", nil, "Branch patterns allowed to deploy here (comma separated)")
	f.StringSlice("tags", nil, "Tag patterns allowed to deploy here")
	f.StringSlice("reviewers", nil, "Approvers: user:EMAIL|ID or role:admin|deployer (comma separated)")
	f.Int("approvals", 0, "Approvals required (0-5)")
	f.Int("wait-minutes", 0, "Minutes to wait after approval before deploying")
	f.StringArray("window", nil, "Deploy window \"mon-fri 09:00-18:00 America/Lima\" (repeatable)")
	f.Bool("auto-deploy", false, "Deploy automatically when a push matches the branch patterns")
	f.Bool("allow-self-approval", false, "Let the requester approve their own promotion")
	f.Bool("require-checks", false, "Require passing checks on the SHA")
	f.String("from-env", "", "Only accept SHAs deployed to this environment first (empty to clear)")
}

func envBody(cmd *cobra.Command, org string) (rec, error) {
	f := cmd.Flags()
	b := rec{}
	if f.Changed("kind") {
		k, _ := f.GetString("kind")
		if k != "staging" && k != "production" && k != "other" {
			return nil, usageErr("--kind must be staging, production or other")
		}
		b["kind"] = k
	}
	if f.Changed("target") {
		t, _ := f.GetString("target")
		if len(t) < 36 {
			id, err := resolveProjectID(org, t)
			if err != nil {
				return nil, usageErr("--target: %v", err)
			}
			t = id
		}
		b["target_project_id"] = t
	}
	if f.Changed("branches") {
		b["branch_patterns"] = csvFlag(cmd, "branches")
	}
	if f.Changed("tags") {
		b["tag_patterns"] = csvFlag(cmd, "tags")
	}
	if f.Changed("reviewers") {
		rv, err := parseReviewers(cmd, csvFlag(cmd, "reviewers"))
		if err != nil {
			return nil, err
		}
		b["reviewers"] = rv
	}
	if f.Changed("approvals") {
		n, _ := f.GetInt("approvals")
		if n < 0 || n > 5 {
			return nil, usageErr("--approvals must be between 0 and 5")
		}
		b["required_approvals"] = n
	}
	if f.Changed("wait-minutes") {
		n, _ := f.GetInt("wait-minutes")
		if n < 0 || n > 43200 {
			return nil, usageErr("--wait-minutes must be between 0 and 43200")
		}
		b["wait_minutes"] = n
	}
	if f.Changed("window") {
		specs, _ := f.GetStringArray("window")
		var ws []rec
		for _, s := range specs {
			w, err := parseWindow(s)
			if err != nil {
				return nil, err
			}
			ws = append(ws, w)
		}
		b["windows"] = ws
	}
	for flag, key := range map[string]string{"auto-deploy": "auto_deploy", "allow-self-approval": "allow_self_approval", "require-checks": "require_checks_on_sha"} {
		if f.Changed(flag) {
			v, _ := f.GetBool(flag)
			b[key] = v
		}
	}
	if f.Changed("from-env") {
		v, _ := f.GetString("from-env")
		if v == "" {
			b["require_source_environment"] = nil
		} else {
			b["require_source_environment"] = v
		}
	}
	return b, nil
}

func deployEnvPath(cmd *cobra.Command, name, suffix string) (string, error) {
	return pp(cmd, "/environments/"+seg(name)+suffix)
}

func printEnv(e rec) {
	fmt.Printf("%s  (%s)\n", sv(e, "name"), sv(e, "kind"))
	for _, k := range []string{"target_project_id", "branch_patterns", "tag_patterns", "required_approvals", "wait_minutes", "auto_deploy", "allow_self_approval", "require_checks_on_sha", "require_source_environment"} {
		if v := sv(e, k); v != "" {
			fmt.Printf("  %-26s %s\n", k+":", v)
		}
	}
	for _, r := range asList(e, "reviewers") {
		fmt.Printf("  reviewer: %s %s\n", sv(r, "type"), sv(r, "user_id", "role"))
	}
	for _, w := range asList(e, "windows") {
		fmt.Printf("  window: %s %s-%s %s\n", sv(w, "days"), sv(w, "from"), sv(w, "to"), sv(w, "tz"))
	}
	if cur, ok := e["current"].(map[string]any); ok && cur != nil {
		fmt.Printf("  current: %s (%s)\n", shortSHA(sv(cur, "sha")), sv(cur, "status"))
	}
}

func init() {
	list := leaf("list", "List the deployment environments", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := pp(cmd, "/environments")
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
		if quietIDs(cmd, items, "name") {
			return nil
		}
		t := newTable(cmd, "NAME", "KIND", "APPROVALS", "BRANCHES", "CURRENT", "STATUS")
		for _, e := range items {
			t.row(sv(e, "name"), sv(e, "kind"), sv(e, "required_approvals"), sv(e, "branch_patterns"), shortSHA(sv(e, "current.sha")), sv(e, "current.status"))
		}
		return t.flush()
	})
	list.Flags().BoolP("quiet", "q", false, "Print only names")

	show := leaf("show NAME", "Show an environment", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, err := deployEnvPath(cmd, args[0], "")
		if err != nil {
			return err
		}
		var e rec
		if _, err := apiDo("GET", p, nil, &e, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, e); done || err != nil {
			return err
		}
		printEnv(e)
		return nil
	})

	create := leaf("create NAME", "Create an environment", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		if !envNameRe.MatchString(args[0]) {
			return usageErr("environment names are lowercase letters, digits and dashes (2-31 characters), starting with a letter; got %q", args[0])
		}
		org, err := resolveOrg(cmd)
		if err != nil {
			return err
		}
		body, err := envBody(cmd, org)
		if err != nil {
			return err
		}
		if _, ok := body["kind"]; !ok {
			return usageErr("--kind is required (staging, production or other)")
		}
		body["name"] = args[0]
		p, err := pp(cmd, "/environments")
		if err != nil {
			return err
		}
		var e rec
		if _, err := apiDo("POST", p, body, &e, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, e); done || err != nil {
			return err
		}
		fmt.Printf("Environment %s created.\n", args[0])
		return nil
	})
	create.Example = `  capsule environments create production --kind production --target web-prod --branches main,release/** --reviewers user:ana@example.com,role:admin --approvals 1 --window "mon-fri 09:00-18:00 America/Lima"`
	addEnvFlags(create)

	set := leaf("set NAME", "Change an environment (only the given flags change)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		org, err := resolveOrg(cmd)
		if err != nil {
			return err
		}
		upd, err := envBody(cmd, org)
		if err != nil {
			return err
		}
		if len(upd) == 0 {
			return usageErr("nothing to change: pass at least one flag")
		}
		p, err := deployEnvPath(cmd, args[0], "")
		if err != nil {
			return err
		}
		var cur rec
		if _, err := apiDo("GET", p, nil, &cur, nil); err != nil {
			return err
		}
		merged := rec{}
		for k, v := range cur {
			switch k {
			case "id", "current", "created_at":
			default:
				merged[k] = v
			}
		}
		for k, v := range upd {
			merged[k] = v
		}
		var out rec
		if _, err := apiDo("PUT", p, merged, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Environment %s updated.\n", args[0])
		return nil
	})
	addEnvFlags(set)

	del := leaf("delete NAME", "Delete an environment", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := confirmOr(cmd, "Delete environment "+args[0]+"?"); err != nil {
			return err
		}
		p, err := deployEnvPath(cmd, args[0], "")
		if err != nil {
			return err
		}
		if _, err := apiDo("DELETE", p, nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("Environment %s deleted.\n", args[0])
		return nil
	})
	addYes(del)

	history := leaf("history NAME", "Deployments and promotions of an environment", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, err := deployEnvPath(cmd, args[0], "/deployments")
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
		t := newTable(cmd, "ID", "SHA", "STATUS", "PROMOTION", "WHEN")
		for _, d := range items {
			t.row(short8(sv(d, "id")), shortSHA(sv(d, "sha")), sv(d, "status"), short8(sv(d, "promotion_id")), age(sv(d, "created_at")))
		}
		return t.flush()
	})
	addList(history)

	g := group("environments", "Deployment environments with approvals and windows", list, show, create, set, del, history)
	g.Aliases = []string{"envs"}
	rootCmd.AddCommand(g)
}
