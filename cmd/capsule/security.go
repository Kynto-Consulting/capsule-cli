package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ── capsule security ─────────────────────────────────────────────────────────
// CLI.md 3.5. operationIds: securityOverview alertsList alertGet alertDismiss
// alertReopen suppressionsList suppressionCreate suppressionDelete
// securityPolicyGet securityPolicyPut scanTrigger sarifUpload sarifStatus
// renovateGet renovatePut renovateRun.

const maxSarifCompressed = 10 << 20 // 10 MiB, as the contract says

var dismissReasons = map[string]bool{"false_positive": true, "wont_fix": true, "used_in_tests": true, "risk_accepted": true}

func normReason(s string) string { return strings.ReplaceAll(strings.ToLower(s), "-", "_") }

// expiryTime turns --expires 90d into an RFC 3339 instant.
func expiryTime(cmd *cobra.Command) (string, error) {
	s, _ := cmd.Flags().GetString("expires")
	if s == "" {
		return "", usageErr("--expires is required: expiry and justification are mandatory")
	}
	d, err := parseDays(s)
	if err != nil {
		return "", err
	}
	if d <= 0 {
		return "", usageErr("--expires must be in the future")
	}
	return nowUTC().Add(d).Format(time.RFC3339), nil
}

func whyText(cmd *cobra.Command) (string, error) {
	w, _ := cmd.Flags().GetString("why")
	if strings.TrimSpace(w) == "" {
		return "", usageErr("--why is required: expiry and justification are mandatory")
	}
	if len([]rune(strings.TrimSpace(w))) < 10 {
		return "", usageErr("--why must be at least 10 characters")
	}
	return strings.TrimSpace(w), nil
}

func alertRows(cmd *cobra.Command, items []rec) error {
	t := newTable(cmd, "ID", "SEV", "KIND", "TOOL", "STATE", "WHERE", "TITLE")
	for _, a := range items {
		where := sv(a, "location.path")
		if l := sv(a, "location.start_line"); l != "" && where != "" {
			where += ":" + l
		}
		if where == "" {
			where = sv(a, "location.package")
		}
		t.row(short8(sv(a, "id")), sv(a, "severity"), sv(a, "kind"), sv(a, "tool"), sv(a, "state"), where, truncate(sv(a, "title"), 50))
	}
	return t.flush()
}

func parseBlockMergeOn(s string) (rec, error) {
	out := rec{}
	for _, kv := range splitCSV(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, usageErr("--block-merge-on expects dependency=high,code=high,secret=any; got %q", kv)
		}
		switch k {
		case "dependency", "code":
			switch v {
			case "off", "critical", "high", "medium", "low":
			default:
				return nil, usageErr("%s must be off, critical, high, medium or low; got %q", k, v)
			}
		case "secret":
			if v != "off" && v != "any" {
				return nil, usageErr("secret must be off or any; got %q", v)
			}
		default:
			return nil, usageErr("unknown kind %q (dependency, code, secret)", k)
		}
		out[k] = v
	}
	return out, nil
}

func init() {
	overview := leaf("overview", "Counts by severity and kind, last scan and whether the policy is blocking", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := pp(cmd, "/security/overview")
		if err != nil {
			return err
		}
		var ov rec
		if _, err := apiDo("GET", p, nil, &ov, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, ov); done || err != nil {
			return err
		}
		fmt.Printf("open: %s   dismissed: %s   last scan: %s   policy blocking: %s\n", orDash(sv(ov, "open")), orDash(sv(ov, "dismissed")), orDash(age(sv(ov, "last_scan_at"))), yn(ov["policy_blocking"] == true))
		for _, g := range []string{"by_severity", "by_kind"} {
			if m, ok := ov[g].(map[string]any); ok {
				fmt.Printf("%s: ", g)
				for _, k := range sortedKeys(m) {
					fmt.Printf("%s=%s ", k, fmtVal(m[k]))
				}
				fmt.Println()
			}
		}
		return nil
	})

	// ── alerts ───────────────────────────────────────────────────────────────
	alist := leaf("list", "List alerts", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		kind, _ := f.GetString("kind")
		sev, _ := f.GetString("severity")
		state, _ := f.GetString("state")
		branch, _ := f.GetString("branch")
		tool, _ := f.GetString("tool")
		p, err := pp(cmd, "/security/alerts"+q("kind", kind, "severity", sev, "state", state, "branch", branch, "tool", tool))
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
		return alertRows(cmd, items)
	})
	alist.Aliases = []string{"ls"}
	alist.Flags().String("kind", "", "dependency, code or secret")
	alist.Flags().String("severity", "", "critical, high, medium, low or info")
	alist.Flags().String("state", "open", "open, fixed or dismissed")
	alist.Flags().String("branch", "", "Branch")
	alist.Flags().String("tool", "", "osv-scanner, semgrep, gitleaks, trivy, ...")
	addList(alist)

	alertPath := func(cmd *cobra.Command, ref, suffix string) (string, string, error) {
		lp, err := pp(cmd, "/security/alerts")
		if err != nil {
			return "", "", err
		}
		id, err := resolveShort(lp, ref, "alert")
		if err != nil {
			return "", "", err
		}
		return lp + "/" + seg(id) + suffix, id, nil
	}
	ashow := leaf("show ID", "Show an alert", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, _, err := alertPath(cmd, args[0], "")
		if err != nil {
			return err
		}
		var a rec
		if _, err := apiDo("GET", p, nil, &a, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, a); done || err != nil {
			return err
		}
		fmt.Printf("%s  [%s] %s\n  %s\n", short8(sv(a, "id")), sv(a, "severity"), sv(a, "state"), sv(a, "title"))
		for _, k := range []string{"kind", "tool", "rule_id", "branch", "first_seen_sha"} {
			if v := sv(a, k); v != "" {
				fmt.Printf("  %-14s %s\n", k+":", v)
			}
		}
		if loc, ok := a["location"].(map[string]any); ok {
			for _, k := range []string{"path", "start_line", "end_line", "package", "version", "fixed_version", "advisory"} {
				if v := sv(loc, k); v != "" {
					fmt.Printf("  %-14s %s\n", k+":", v)
				}
			}
		}
		if d, ok := a["dismissal"].(map[string]any); ok && d != nil {
			fmt.Printf("  dismissed: %s (%s) by %s until %s\n", sv(d, "reason"), sv(d, "justification"), sv(d, "by"), sv(d, "expires_at"))
		}
		return nil
	})
	adismiss := leaf("dismiss ID", "Dismiss an alert with a reason, justification and expiry", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		reason, _ := cmd.Flags().GetString("reason")
		reason = normReason(reason)
		if !dismissReasons[reason] {
			return usageErr("--reason must be false_positive, wont_fix, used_in_tests or risk_accepted")
		}
		why, err := whyText(cmd)
		if err != nil {
			return err
		}
		exp, err := expiryTime(cmd)
		if err != nil {
			return err
		}
		p, id, err := alertPath(cmd, args[0], "/dismiss")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{"reason": reason, "justification": why, "expires_at": exp}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Alert %s dismissed (%s) until %s.\n", short8(id), reason, exp)
		return nil
	})
	adismiss.Flags().String("reason", "", "false_positive, wont_fix, used_in_tests or risk_accepted")
	adismiss.Flags().String("why", "", "Justification (at least 10 characters)")
	adismiss.Flags().String("expires", "", "How long the dismissal lasts, e.g. 90d")
	areopen := leaf("reopen ID", "Reopen a dismissed alert", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, id, err := alertPath(cmd, args[0], "/reopen")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{}, &out, nil); err != nil {
			return err
		}
		fmt.Printf("Alert %s reopened.\n", short8(id))
		return nil
	})
	alerts := group("alerts", "Security alerts", alist, ashow, adismiss, areopen)

	// ── suppressions ─────────────────────────────────────────────────────────
	slist := leaf("list", "List suppressions", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := pp(cmd, "/security/suppressions")
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
		t := newTable(cmd, "ID", "MATCH", "REASON", "EXPIRES", "STATE")
		for _, s := range items {
			m, _ := s["match"].(map[string]any)
			match := sv(m, "rule_id")
			for _, k := range []string{"fingerprint", "path_glob"} {
				if v := sv(m, k); v != "" {
					match += " " + k + "=" + v
				}
			}
			t.row(short8(sv(s, "id")), strings.TrimSpace(match), sv(s, "reason"), sv(s, "expires_at"), sv(s, "state"))
		}
		return t.flush()
	})
	slist.Flags().BoolP("quiet", "q", false, "Print only IDs")
	sadd := leaf("add", "Suppress findings by rule, fingerprint or path (needs --why and --expires)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		match := rec{}
		for flag, key := range map[string]string{"rule-id": "rule_id", "fingerprint": "fingerprint", "path-glob": "path_glob"} {
			if v, _ := f.GetString(flag); v != "" {
				match[key] = v
			}
		}
		if len(match) == 0 {
			return usageErr("pass at least one of --rule-id, --fingerprint, --path-glob")
		}
		reason, _ := f.GetString("reason")
		reason = normReason(reason)
		if !dismissReasons[reason] {
			return usageErr("--reason must be false_positive, wont_fix, used_in_tests or risk_accepted")
		}
		why, err := whyText(cmd)
		if err != nil {
			return err
		}
		exp, err := expiryTime(cmd)
		if err != nil {
			return err
		}
		p, err := pp(cmd, "/security/suppressions")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{"match": match, "reason": reason, "justification": why, "expires_at": exp}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Suppression %s created until %s.\n", short8(sv(out, "id")), exp)
		return nil
	})
	sadd.Flags().String("rule-id", "", "Rule id to suppress")
	sadd.Flags().String("fingerprint", "", "Finding fingerprint")
	sadd.Flags().String("path-glob", "", "Path glob")
	sadd.Flags().String("reason", "", "false_positive, wont_fix, used_in_tests or risk_accepted")
	sadd.Flags().String("why", "", "Justification (at least 10 characters)")
	sadd.Flags().String("expires", "", "How long it lasts, e.g. 90d")
	srem := leaf("remove ID", "Remove a suppression", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		lp, err := pp(cmd, "/security/suppressions")
		if err != nil {
			return err
		}
		id, err := resolveShort(lp, args[0], "suppression")
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Remove suppression "+short8(id)+"? Its findings reappear."); err != nil {
			return err
		}
		if _, err := apiDo("DELETE", lp+"/"+seg(id), nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("Suppression %s removed.\n", short8(id))
		return nil
	})
	addYes(srem)
	supp := group("suppressions", "Time-limited suppressions", slist, sadd, srem)

	// ── policy ───────────────────────────────────────────────────────────────
	pshow := leaf("show", "Show the security policy", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := pp(cmd, "/security/policy")
		if err != nil {
			return err
		}
		var pol rec
		if _, err := apiDo("GET", p, nil, &pol, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, pol); done || err != nil {
			return err
		}
		if m, ok := pol["block_merge_on"].(map[string]any); ok {
			fmt.Printf("block merge on:   dependency=%s code=%s secret=%s\n", orDash(sv(m, "dependency")), orDash(sv(m, "code")), orDash(sv(m, "secret")))
		}
		fmt.Printf("block deploy on:  %s\n", orDash(sv(pol, "block_deploy_on")))
		fmt.Printf("required tools:   %s\n", orDash(sv(pol, "required_tools")))
		fmt.Printf("max suppression:  %s days\n", orDash(sv(pol, "max_suppression_days")))
		fmt.Printf("branches:         %s\n", orDash(sv(pol, "applies_to_branches")))
		return nil
	})
	pset := leaf("set", "Change the security policy (only the given parts change)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		p, err := pp(cmd, "/security/policy")
		if err != nil {
			return err
		}
		var pol rec
		if _, err := apiDo("GET", p, nil, &pol, nil); err != nil {
			return err
		}
		changed := false
		if f.Changed("block-merge-on") {
			s, _ := f.GetString("block-merge-on")
			upd, err := parseBlockMergeOn(s)
			if err != nil {
				return err
			}
			cur, _ := pol["block_merge_on"].(map[string]any)
			merged := rec{}
			for k, v := range cur {
				merged[k] = v
			}
			for k, v := range upd {
				merged[k] = v
			}
			pol["block_merge_on"] = merged
			changed = true
		}
		if f.Changed("block-deploy-on") {
			s, _ := f.GetString("block-deploy-on")
			if s != "off" && s != "critical" && s != "high" {
				return usageErr("--block-deploy-on must be off, critical or high")
			}
			pol["block_deploy_on"] = s
			changed = true
		}
		if f.Changed("require-tools") {
			pol["required_tools"] = csvFlag(cmd, "require-tools")
			changed = true
		}
		if f.Changed("max-suppression-days") {
			n, _ := f.GetInt("max-suppression-days")
			if n < 1 {
				return usageErr("--max-suppression-days must be at least 1")
			}
			pol["max_suppression_days"] = n
			changed = true
		}
		if f.Changed("branches") {
			pol["applies_to_branches"] = csvFlag(cmd, "branches")
			changed = true
		}
		if !changed {
			return usageErr("nothing to change: pass --block-merge-on, --block-deploy-on, --require-tools, --max-suppression-days or --branches")
		}
		var out rec
		if _, err := apiDo("PUT", p, pol, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Println("Security policy updated.")
		return nil
	})
	pset.Flags().String("block-merge-on", "", "dependency=high,code=high,secret=any (levels: off, critical, high, medium, low; secret: off, any)")
	pset.Flags().String("block-deploy-on", "", "off, critical or high")
	pset.Flags().StringSlice("require-tools", nil, "Tools that must have reported: semgrep,gitleaks,...")
	pset.Flags().Int("max-suppression-days", 0, "Longest allowed suppression")
	pset.Flags().StringSlice("branches", nil, "Branch patterns the policy applies to")
	policy := group("policy", "Security policy: what blocks merging and deploying", pshow, pset)

	// ── scan ─────────────────────────────────────────────────────────────────
	scan := leaf("scan", "Start a scan", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		body := rec{}
		if t := csvFlag(cmd, "tools"); len(t) > 0 {
			body["tools"] = t
		}
		if r, _ := f.GetString("ref"); r != "" {
			body["ref"] = r
		}
		p, err := pp(cmd, "/security/scans")
		if err != nil {
			return err
		}
		op, _ := pp(cmd, "/security/overview")
		var before rec
		if waitDuration(cmd) > 0 {
			_, _ = apiDo("GET", op, nil, &before, nil)
		}
		start := nowUTC().Add(-5 * time.Second)
		var out rec
		if _, err := apiDo("POST", p, body, &out, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if w := waitDuration(cmd); w > 0 {
			err := pollUntil(w, "scan", func() (bool, string, error) {
				var ov rec
				if _, err := apiDo("GET", op, nil, &ov, nil); err != nil {
					return false, "", err
				}
				t, e := time.Parse(time.RFC3339, sv(ov, "last_scan_at"))
				return e == nil && t.After(start), "scanning", nil
			})
			if err != nil {
				return err
			}
			fmt.Println("Scan finished.")
			return nil
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Println("Scan requested.")
		return nil
	})
	scan.Flags().StringSlice("tools", nil, "osv-scanner, semgrep, gitleaks, trivy (default: all configured)")
	scan.Flags().String("ref", "", "Branch or SHA to scan (default: the default branch)")
	addWait(scan, "the scan")
	addIdem(scan)

	// ── sarif ────────────────────────────────────────────────────────────────
	supload := leaf("upload FILE", "Upload a SARIF 2.1.0 file (gzip + base64 automatically; token scope security:write)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		sha, _ := f.GetString("sha")
		if sha == "" {
			sha = firstEnv("GITHUB_SHA", "CAPSULE_GIT_SHA")
		}
		ref, _ := f.GetString("ref")
		if ref == "" {
			ref = firstEnv("GITHUB_REF_NAME", "CAPSULE_GIT_BRANCH")
		}
		if sha == "" || ref == "" {
			return usageErr("--sha and --ref are required (they default to GITHUB_SHA and GITHUB_REF_NAME in CI)")
		}
		raw, err := os.ReadFile(args[0])
		if err != nil {
			return usageErr("reading the SARIF file: %v", err)
		}
		if !bytes.Contains(raw, []byte(`"runs"`)) {
			return usageErr("%s does not look like a SARIF file (no \"runs\")", args[0])
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		if buf.Len() > maxSarifCompressed {
			return usageErr("the SARIF file is %d bytes compressed; the limit is 10 MiB", buf.Len())
		}
		body := rec{"commit_sha": sha, "ref": ref, "sarif": base64.StdEncoding.EncodeToString(buf.Bytes())}
		if t, _ := f.GetString("tool"); t != "" {
			body["tool_name"] = t
		}
		if pr, _ := f.GetInt("pr"); pr > 0 {
			body["pr"] = pr
		}
		p, err := pp(cmd, "/security/sarif")
		if err != nil {
			return err
		}
		var res rec
		if _, err := apiDo("POST", p, body, &res, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if w := waitDuration(cmd); w > 0 && sv(res, "id") != "" {
			sp := p + "/" + seg(sv(res, "id"))
			err := pollUntil(w, "SARIF processing", func() (bool, string, error) {
				res = rec{}
				if _, err := apiDo("GET", sp, nil, &res, nil); err != nil {
					return false, "", err
				}
				st := sv(res, "status")
				return st == "complete" || st == "failed", st, nil
			})
			if err != nil {
				return err
			}
			if sv(res, "status") == "failed" {
				return codedErr(exitGeneric, "SARIF processing failed: %s", orDash(sv(res, "error")))
			}
		}
		if done, err := emit(cmd, res); done || err != nil {
			return err
		}
		fmt.Printf("SARIF upload %s: %s (alerts created: %s, fixed: %s)\n", short8(sv(res, "id")), orDash(sv(res, "status")), orDash(sv(res, "alerts_created")), orDash(sv(res, "alerts_fixed")))
		return nil
	})
	supload.Flags().String("sha", "", "Commit SHA the results belong to (default: GITHUB_SHA)")
	supload.Flags().String("ref", "", "Branch or ref (default: GITHUB_REF_NAME)")
	supload.Flags().String("tool", "", "Tool name shown on the alerts")
	supload.Flags().Int("pr", 0, "Pull request number, when the scan is for a PR")
	addWait(supload, "the upload to be processed")
	addIdem(supload)
	sstatus := leaf("status ID", "Show the processing state of an upload", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, err := pp(cmd, "/security/sarif/"+seg(args[0]))
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
		fmt.Printf("%s: %s (created %s, fixed %s) %s\n", short8(sv(res, "id")), sv(res, "status"), orDash(sv(res, "alerts_created")), orDash(sv(res, "alerts_fixed")), sv(res, "error"))
		return nil
	})
	sarif := group("sarif", "SARIF uploads (used from CI)", supload, sstatus)

	// ── renovate ─────────────────────────────────────────────────────────────
	rshow := leaf("show", "Show Renovate's state for the repository", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := pp(cmd, "/security/renovate")
		if err != nil {
			return err
		}
		var r rec
		if _, err := apiDo("GET", p, nil, &r, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, r); done || err != nil {
			return err
		}
		fmt.Printf("enabled: %s   onboarding: %s   open PRs: %s   last run: %s\n", yn(r["enabled"] == true), orDash(sv(r, "onboarding")), orDash(sv(r, "open_prs")), orDash(age(sv(r, "last_run_at"))))
		if u := sv(r, "dashboard_issue_url"); u != "" {
			fmt.Println("dashboard:", u)
		}
		return nil
	})
	rput := func(enabled bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			body := rec{"enabled": enabled}
			if s, _ := cmd.Flags().GetString("preset"); s != "" {
				body["preset"] = s
			}
			if s, _ := cmd.Flags().GetString("schedule"); s != "" {
				body["schedule"] = s
			}
			p, err := pp(cmd, "/security/renovate")
			if err != nil {
				return err
			}
			var out rec
			if _, err := apiDo("PUT", p, body, &out, nil); err != nil {
				return err
			}
			if done, err := emit(cmd, out); done || err != nil {
				return err
			}
			fmt.Printf("Renovate %s.\n", map[bool]string{true: "enabled", false: "disabled"}[enabled])
			return nil
		}
	}
	renable := leaf("enable", "Enable Renovate", cobra.NoArgs, rput(true))
	renable.Flags().String("preset", "", "Renovate preset")
	renable.Flags().String("schedule", "", "Schedule expression")
	rdisable := leaf("disable", "Disable Renovate", cobra.NoArgs, rput(false))
	rrun := leaf("run", "Run Renovate now", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := pp(cmd, "/security/renovate/run")
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
		fmt.Println("Renovate run requested.")
		return nil
	})
	addIdem(rrun)
	renovate := group("renovate", "Renovate dependency update PRs", rshow, renable, rdisable, rrun)

	rootCmd.AddCommand(group("security", "Security: alerts, suppressions, policy, scans, SARIF, Renovate", overview, alerts, supp, policy, scan, sarif, renovate))
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

var _ = strconv.Itoa
