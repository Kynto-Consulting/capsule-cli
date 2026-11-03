package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// ── capsule approvals ────────────────────────────────────────────────────────
// CLI.md 3.6. operationIds: approvalsPending promotionApprove promotionReject.

// approvalProject finds the project a pending promotion belongs to: the
// --project flag, CAPSULE_PROJECT_ID or .capsule.json when present, otherwise
// the project_id the pending list reports for it.
func approvalProject(cmd *cobra.Command, item rec) (string, error) {
	p, _ := cmd.Flags().GetString("project")
	if p == "" {
		_, p = envOrgProject()
	}
	if p == "" {
		if pc, _, err := config.FindProjectConfig(cwdForConfig()); err == nil {
			p = pc.ProjectID
		}
	}
	if p == "" {
		p = sv(item, "project_id", "project.id")
	}
	if p == "" {
		return "", usageErr("which project? pass --project ID (or run inside a linked project)")
	}
	if len(p) < 36 {
		o, err := resolveOrg(cmd)
		if err != nil {
			return "", err
		}
		id, err := resolveProjectID(o, p)
		if err != nil {
			return "", usageErr("%v", err)
		}
		p = id
	}
	return p, nil
}

func decide(cmd *cobra.Command, args []string, verb, action string) error {
	msg, _ := cmd.Flags().GetString("message")
	if strings.TrimSpace(msg) == "" {
		return usageErr("-m TEXT is required: say why you %s it", verb)
	}
	o, err := resolveOrg(cmd)
	if err != nil {
		return err
	}
	var raw any
	if _, err := apiDo("GET", "/api/v1/orgs/"+o+"/approvals", nil, &raw, nil); err != nil {
		return err
	}
	items, _ := pageOf(raw)
	var item rec
	if len(args[0]) >= 36 {
		item = rec{"id": args[0]}
		for _, it := range items {
			if sv(it, "id") == args[0] {
				item = it
			}
		}
	} else {
		item, err = pickByPrefix(items, args[0], "pending promotion", "id")
		if err != nil {
			return usageErr("%v", err)
		}
	}
	proj, err := approvalProject(cmd, item)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/promotions/%s/%s", o, proj, seg(sv(item, "id")), action)
	var out rec
	if _, err := apiDo("POST", path, rec{"comment": msg}, &out, nil); err != nil {
		return err
	}
	if done, err := emit(cmd, out); done || err != nil {
		return err
	}
	if action == "approve" {
		fmt.Printf("Approved %s. Deploying once the remaining rules are met.\n", short8(sv(item, "id")))
	} else {
		fmt.Printf("Rejected %s.\n", short8(sv(item, "id")))
	}
	return nil
}

func init() {
	list := orgLeaf("list", "Promotions waiting for your approval", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		p, err := op(cmd, "/approvals")
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
		t := newTable(cmd, "ID", "ENVIRONMENT", "SHA", "REQUESTED BY", "APPROVALS", "EXPIRES")
		for _, pr := range items {
			t.row(short8(sv(pr, "id")), sv(pr, "environment"), shortSHA(sv(pr, "sha")), sv(pr, "requested_by"), fmt.Sprintf("%d/%s", countDecision(pr, "approved"), orDash(sv(pr, "required_approvals"))), sv(pr, "expires_at"))
		}
		return t.flush()
	})
	addList(list)
	approve := orgLeaf("approve ID", "Approve a promotion", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return decide(cmd, args, "approve", "approve")
	})
	reject := orgLeaf("reject ID", "Reject a promotion", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return decide(cmd, args, "reject", "reject")
	})
	for _, c := range []*cobra.Command{approve, reject} {
		c.Flags().StringP("message", "m", "", "Comment recorded with the decision")
		c.Flags().String("project", "", "Project ID (only needed when the pending list does not say)")
	}
	rootCmd.AddCommand(group("approvals", "Approve or reject promotions to protected environments", list, approve, reject))
}
