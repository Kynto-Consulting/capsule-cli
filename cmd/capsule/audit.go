package main

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ── capsule audit ────────────────────────────────────────────────────────────
// CLI.md 3.7. operationId: auditLog.

func sinceTime(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return s, nil
	}
	d, err := parseDays(s)
	if err != nil {
		return "", err
	}
	return nowUTC().Add(-d).Format(time.RFC3339), nil
}

func init() {
	c := orgLeaf("audit", "Unified audit log of the organization", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		action, _ := f.GetString("action")
		actor, _ := f.GetString("actor")
		project, _ := f.GetString("project")
		since, _ := f.GetString("since")
		until, _ := f.GetString("until")
		since, err := sinceTime(since)
		if err != nil {
			return err
		}
		if until != "" {
			if u, err := sinceTime(until); err == nil {
				until = u
			}
		}
		org, err := resolveOrg(cmd)
		if err != nil {
			return err
		}
		if actor != "" {
			if actor == "@me" {
				if actor, err = meLogin(); err != nil {
					return err
				}
			} else if strings.Contains(actor, "@") {
				if id, err := resolveUser(cmd, actor); err == nil {
					actor = id
				}
			}
		}
		if project != "" && len(project) < 36 {
			id, err := resolveProjectID(org, project)
			if err != nil {
				return usageErr("--project: %v", err)
			}
			project = id
		}
		p := "/api/v1/orgs/" + org + "/audit-log" + q("action", action, "actor", actor, "project", project, "since", since, "until", until)
		items, raw, err := fetchList(cmd, p)
		if err != nil {
			return err
		}
		if csvOut, _ := f.GetBool("csv"); csvOut {
			w := csv.NewWriter(os.Stdout)
			_ = w.Write([]string{"time", "action", "actor", "resource_type", "resource_id", "ip", "old_values", "new_values"})
			for _, e := range items {
				_ = w.Write([]string{sv(e, "created_at"), sv(e, "action"), sv(e, "actor"), sv(e, "resource_type"), sv(e, "resource_id"), sv(e, "ip"), jsonCell(e["old_values"]), jsonCell(e["new_values"])})
			}
			w.Flush()
			return w.Error()
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		if quietIDs(cmd, items) {
			return nil
		}
		t := newTable(cmd, "TIME", "ACTION", "ACTOR", "RESOURCE", "IP")
		for _, e := range items {
			t.row(sv(e, "created_at"), sv(e, "action"), sv(e, "actor"), sv(e, "resource_type")+" "+short8(sv(e, "resource_id")), sv(e, "ip"))
		}
		if err := t.flush(); err != nil {
			return err
		}
		return nil
	})
	c.Flags().String("action", "", "Action name filter, e.g. git.pr.merge")
	c.Flags().String("actor", "", "User id, email or @me")
	c.Flags().String("project", "", "Project id or slug")
	c.Flags().String("since", "", "Start: RFC 3339 time or a relative age such as 7d")
	c.Flags().String("until", "", "End: RFC 3339 time or a relative age")
	c.Flags().Bool("csv", false, "Print CSV (for spreadsheets and SIEM imports)")
	addList(c)
	rootCmd.AddCommand(c)
}

func jsonCell(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
