package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ── capsule promote ──────────────────────────────────────────────────────────
// CLI.md 3.6. operationIds: promotionCreate promotionGet promotionCancel.

// resolvePromotionID expands a short promotion id using the pending approvals
// first, then the deployment history of each environment of the project.
func resolvePromotionID(cmd *cobra.Command, ref string) (string, error) {
	if len(ref) >= 36 {
		return ref, nil
	}
	if o, err := resolveOrg(cmd); err == nil {
		var raw any
		if _, err := apiDo("GET", "/api/v1/orgs/"+o+"/approvals", nil, &raw, nil); err == nil {
			items, _ := pageOf(raw)
			if r, err := pickByPrefix(items, ref, "promotion", "id"); err == nil {
				return sv(r, "id"), nil
			}
		}
	}
	envsPath, err := pp(cmd, "/environments")
	if err != nil {
		return "", err
	}
	var raw any
	if _, err := apiDo("GET", envsPath, nil, &raw, nil); err != nil {
		return "", err
	}
	envs, _ := pageOf(raw)
	var hits []string
	for _, e := range envs {
		var dr any
		if _, err := apiDo("GET", envsPath+"/"+seg(sv(e, "name"))+"/deployments", nil, &dr, nil); err != nil {
			continue
		}
		ds, _ := pageOf(dr)
		for _, d := range ds {
			if id := sv(d, "promotion_id"); id != "" && strings.HasPrefix(id, ref) {
				dup := false
				for _, h := range hits {
					dup = dup || h == id
				}
				if !dup {
					hits = append(hits, id)
				}
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", usageErr("no promotion with id starting %q (pass the full id)", ref)
	case 1:
		return hits[0], nil
	}
	return "", usageErr("promotion id prefix %q is ambiguous (%d match); use more characters", ref, len(hits))
}

func printPromotion(p rec) {
	fmt.Printf("Promotion %s to %s: %s (sha %s)\n", short8(sv(p, "id")), sv(p, "environment"), sv(p, "status"), shortSHA(sv(p, "sha")))
	if sv(p, "status") == "pending_approval" {
		fmt.Printf("  %s approval(s) required, %d given\n", orDash(sv(p, "required_approvals")), countDecision(p, "approved"))
	}
	for _, a := range asList(p, "approvals") {
		fmt.Printf("  %s by %s: %s\n", sv(a, "decision"), sv(a, "user"), sv(a, "comment"))
	}
	if nb := sv(p, "not_before"); nb != "" {
		fmt.Printf("  not before: %s\n", nb)
	}
	if ck, ok := p["checks"].(map[string]any); ok {
		fmt.Printf("  checks: %s\n", sv(ck, "state"))
	}
}

func countDecision(p rec, d string) int {
	n := 0
	for _, a := range asList(p, "approvals") {
		if sv(a, "decision") == d {
			n++
		}
	}
	return n
}

// waitPromotion polls a promotion until it reaches an end state. Pending
// approval returns immediately with exit 8: a person has to act.
func waitPromotion(cmd *cobra.Command, id string, timeout time.Duration) error {
	p, err := pp(cmd, "/promotions/"+seg(id))
	if err != nil {
		return err
	}
	var cur rec
	err = pollUntil(timeout, "promotion "+short8(id), func() (bool, string, error) {
		cur = rec{}
		if _, err := apiDo("GET", p, nil, &cur, nil); err != nil {
			return false, "", err
		}
		st := sv(cur, "status")
		switch st {
		case "pending_approval", "success", "failed", "rejected", "cancelled", "expired":
			return true, st, nil
		}
		return false, st, nil
	})
	if err != nil {
		return err
	}
	printPromotion(cur)
	switch sv(cur, "status") {
	case "success":
		return nil
	case "pending_approval":
		return codedErr(exitApproval, "promotion %s is waiting for approval", short8(id))
	case "rejected":
		return codedErr(exitBlocked, "promotion %s was rejected", short8(id))
	default:
		return codedErr(exitGeneric, "promotion %s ended with status %s", short8(id), sv(cur, "status"))
	}
}

func init() {
	run := func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		to, _ := f.GetString("to")
		sha, _ := f.GetString("sha")
		if to == "" || sha == "" {
			return usageErr("--to ENVIRONMENT and --sha SHA are required")
		}
		body := rec{"environment": to, "sha": sha}
		if from, _ := f.GetString("from"); from != "" {
			body["from_environment"] = from
		}
		if c, _ := f.GetString("comment"); c != "" {
			body["comment"] = c
		}
		p, err := pp(cmd, "/promotions")
		if err != nil {
			return err
		}
		var pr rec
		if _, err := apiDo("POST", p, body, &pr, idemHeaders(cmd, args)); err != nil {
			return err
		}
		wait := waitDuration(cmd)
		if wait > 0 {
			return waitPromotion(cmd, sv(pr, "id"), wait)
		}
		if done, err := emit(cmd, pr); done || err != nil {
			return err
		}
		printPromotion(pr)
		return nil
	}
	promote := leaf("promote", "Promote a commit to an environment (approvals, windows and checks are enforced by the server)", cobra.NoArgs, run)
	promote.Example = `  capsule promote --to production --sha a1b2c3d --wait    # exit 8 while it waits for approval`
	promote.Flags().String("to", "", "Target environment")
	promote.Flags().String("sha", "", "Commit SHA to promote")
	promote.Flags().String("from", "", "Source environment the SHA must have been deployed to")
	promote.Flags().String("comment", "", "Comment for the approvers")
	addWait(promote, "the promotion")
	addIdem(promote)

	show := leaf("show ID", "Show a promotion", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := resolvePromotionID(cmd, args[0])
		if err != nil {
			return err
		}
		p, _ := pp(cmd, "/promotions/"+seg(id))
		var pr rec
		if _, err := apiDo("GET", p, nil, &pr, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, pr); done || err != nil {
			return err
		}
		printPromotion(pr)
		return nil
	})
	cancel := leaf("cancel ID", "Cancel a promotion that has not deployed yet", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := resolvePromotionID(cmd, args[0])
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Cancel promotion "+short8(id)+"?"); err != nil {
			return err
		}
		p, _ := pp(cmd, "/promotions/"+seg(id)+"/cancel")
		var out rec
		if _, err := apiDo("POST", p, rec{}, &out, nil); err != nil {
			return err
		}
		fmt.Printf("Promotion %s cancelled.\n", short8(id))
		return nil
	})
	addYes(cancel)
	promote.AddCommand(show, cancel)
	rootCmd.AddCommand(promote)
}
