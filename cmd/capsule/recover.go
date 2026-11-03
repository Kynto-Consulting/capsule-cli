package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// `capsule recover plan|apply|verify|status` (docs/git-capsule/DR-BACKUP.md section 3, package C3).
//
//	plan   POST /git/admin/recover (--all) or /orgs/{o}/projects/{p}/recover   mode=plan   (read only)
//	apply  same endpoints, mode=apply  (asks first; --force-overwrite and --continue-on-error are explicit)
//	verify same endpoints, mode=verify (changes nothing)
//	status GET /git/admin/recover/{runID}
//
// The server never changes infrastructure (Terraform, RDS, DNS): that part of F1/F2/F7 runs from an
// operator host with deploy/recovery/recover.sh (bootstrap mode), see deploy/recovery/README.md.
func init() {
	registerOps(map[string]string{
		"recoverProject":     "recover apply",
		"gitRecoverPlatform": "recover plan",
		"gitRecoverGet":      "recover status",
	})

	mk := func(mode, short string) *cobra.Command {
		c := leaf(mode, short, cobra.NoArgs, func(cmd *cobra.Command, args []string) error { return runRecover(cmd, args, mode) })
		c.Flags().Bool("all", false, "Recover the whole platform instead of one project (platform administrators)")
		c.Flags().String("snapshot", "", "Pin the snapshot to use (project only; excludes --at)")
		c.Flags().String("at", "", "Point in time, RFC 3339 (uses the newest backups not after it)")
		c.Flags().String("target", "in_place", "in_place or ephemeral (ephemeral never touches DNS, the elastic IP or production)")
		c.Flags().StringSlice("scope", nil, "Phases: network,core,git,databases,volumes,projects,dns (default: all that apply)")
		addIdem(c)
		return c
	}
	plan := mk("plan", "Resolve what would be restored, check backups and list risks (read only)")
	apply := mk("apply", "Run the recovery step by step (idempotent; asks for confirmation)")
	apply.Flags().Bool("force-overwrite", false, "Consent to overwrite existing data (a pre-restore copy is kept)")
	apply.Flags().Bool("continue-on-error", false, "Apply despite blocking risks and keep going after a failed project step")
	addYes(apply)
	addWait(apply, "the recovery (platform runs only)")
	verify := mk("verify", "Re-check backups, checksums and manifests without changing anything")
	status := userLeaf("status RUN_ID", "Show a platform recovery run", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		var out rec
		if _, err := apiDo("GET", "/api/v1/git/admin/recover/"+args[0], nil, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		printRun(out)
		return nil
	})
	rootCmd.AddCommand(group("recover", "Total recovery: plan, apply and verify (DR-BACKUP.md)", plan, apply, verify, status))
}

func recoverPath(cmd *cobra.Command) (string, error) {
	if all, _ := cmd.Flags().GetBool("all"); all {
		return "/api/v1/git/admin/recover", nil
	}
	b, err := projBase(cmd)
	if err != nil {
		return "", usageErr("choose --all or a project (--project, CAPSULE_PROJECT_ID or .capsule.json): %v", err)
	}
	return b + "/recover", nil
}

func recoverBody(cmd *cobra.Command, mode string) (rec, error) {
	body := rec{"mode": mode}
	if v, _ := cmd.Flags().GetString("snapshot"); v != "" {
		body["snapshot_id"] = v
	}
	if v, _ := cmd.Flags().GetString("at"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return nil, usageErr("--at must be RFC 3339, for example 2026-11-01T10:00:00Z")
		}
		body["point_in_time"] = v
	}
	if v, _ := cmd.Flags().GetString("target"); v != "" {
		if v != "in_place" && v != "ephemeral" {
			return nil, usageErr("--target must be in_place or ephemeral")
		}
		body["target"] = v
	}
	if v, _ := cmd.Flags().GetStringSlice("scope"); len(v) > 0 {
		body["scope"] = v
	}
	if cmd.Flags().Lookup("force-overwrite") != nil {
		if v, _ := cmd.Flags().GetBool("force-overwrite"); v {
			body["force_overwrite"] = true
		}
		if v, _ := cmd.Flags().GetBool("continue-on-error"); v {
			body["continue_on_error"] = true
		}
	}
	return body, nil
}

func runRecover(cmd *cobra.Command, args []string, mode string) error {
	path, err := recoverPath(cmd)
	if err != nil {
		return err
	}
	body, err := recoverBody(cmd, mode)
	if err != nil {
		return err
	}
	if mode == "apply" {
		label := "Apply the recovery? Existing data is overwritten (a pre-restore copy is kept)."
		if t, _ := cmd.Flags().GetString("target"); t == "ephemeral" {
			label = "Apply the recovery on an ephemeral target?"
		}
		if err := confirmOr(cmd, label); err != nil {
			return err
		}
	}
	var out rec
	if _, err := apiDo("POST", path, body, &out, idemHeaders(cmd, args)); err != nil {
		return err
	}
	if mode == "apply" && strings.HasSuffix(path, "/git/admin/recover") {
		if d := waitDuration(cmd); d > 0 {
			id := sv(out, "id")
			if err := pollUntil(d, "recovery "+short8(id), func() (bool, string, error) {
				var cur rec
				if _, err := apiDo("GET", "/api/v1/git/admin/recover/"+id, nil, &cur, nil); err != nil {
					return false, "", err
				}
				out = cur
				st := sv(cur, "status")
				return st != "running" && st != "planned", st, nil
			}); err != nil {
				return err
			}
		}
	}
	if done, err := emit(cmd, out); done || err != nil {
		return err
	}
	if mode == "plan" {
		printPlan(out)
		if ready, ok := out["ready"].(bool); ok && !ready {
			return codedErr(exitBlocked, "the plan has blocking risks (severity error): fix them or pass --continue-on-error to apply")
		}
		return nil
	}
	printRun(out)
	switch sv(out, "status") {
	case "failed":
		return codedErr(exitGeneric, "the recovery failed")
	case "partial":
		return codedErr(exitGeneric, "the recovery finished partially: see the failed steps")
	}
	return nil
}

func printPlan(out rec) {
	fmt.Printf("plan %s  steps: %d  estimate: %ss\n", short8(sv(out, "plan_id")), len(asList(out, "steps")), orDash(sv(out, "estimate_seconds")))
	t := newTW()
	for _, s := range asList(out, "steps") {
		fmt.Fprintf(t, "%s\t%s\n", sv(s, "phase"), sv(s, "name"))
	}
	_ = t.Flush()
	if risks := asList(out, "risks"); len(risks) > 0 {
		fmt.Println("\nrisks:")
		for _, r := range risks {
			fmt.Printf("  [%s] %s %s: %s\n", sv(r, "severity"), sv(r, "code"), sv(r, "subject"), sv(r, "message"))
		}
	}
}

func printRun(out rec) {
	fmt.Printf("run %s  mode: %s  status: %s\n", sv(out, "id"), sv(out, "mode"), sv(out, "status"))
	for _, s := range asList(out, "steps") {
		fmt.Printf("  [%s] %s %s\n", sv(s, "status"), sv(s, "name"), sv(s, "detail"))
	}
	if u := sv(out, "report_url"); u != "" {
		fmt.Println("report:", u)
	}
}
