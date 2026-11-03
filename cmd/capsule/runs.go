package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ── capsule runs ─────────────────────────────────────────────────────────────
// CLI.md 3.4. operationIds: runsList runGet jobLogs runRerun runCancel
// artifactsList.

func runTerminal(status string) bool {
	switch status {
	case "success", "failure", "cancelled":
		return true
	}
	return false
}

func runPath(cmd *cobra.Command, ref, suffix string) (string, string, error) {
	lp, err := gp(cmd, "/runs")
	if err != nil {
		return "", "", err
	}
	id, err := resolveShort(lp, ref, "run")
	if err != nil {
		return "", "", err
	}
	return lp + "/" + seg(id) + suffix, id, nil
}

// watchRun polls a run until it ends. With exitStatus a failed run exits 6.
func watchRun(cmd *cobra.Command, id string, timeout time.Duration, exitStatus bool) error {
	if timeout == 0 {
		timeout = 60 * time.Minute
	}
	p, _, err := runPath(cmd, id, "")
	if err != nil {
		return err
	}
	var run rec
	err = pollUntil(timeout, "run "+short8(id), func() (bool, string, error) {
		run = rec{}
		if _, err := apiDo("GET", p, nil, &run, nil); err != nil {
			return false, "", err
		}
		st := sv(run, "status")
		return runTerminal(st), st, nil
	})
	if err != nil {
		return err
	}
	st := sv(run, "status")
	fmt.Printf("Run %s finished: %s\n", short8(id), st)
	if exitStatus && st != "success" {
		return codedErr(exitTimeout, "run %s ended with status %s", short8(id), st)
	}
	return nil
}

// pickJob finds a job by id or name; with no ref it requires a single job.
func pickJob(run rec, ref string) (rec, error) {
	jobs := asList(run, "jobs")
	if ref == "" {
		if len(jobs) == 1 {
			return jobs[0], nil
		}
		var names []string
		for _, j := range jobs {
			names = append(names, sv(j, "name"))
		}
		return nil, usageErr("the run has %d jobs: choose one with --job (%s)", len(jobs), strings.Join(names, ", "))
	}
	for _, j := range jobs {
		if sv(j, "id") == ref || sv(j, "name") == ref {
			return j, nil
		}
	}
	return nil, usageErr("no job %q in this run", ref)
}

func init() {
	list := leaf("list", "List workflow runs", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		wf, _ := f.GetString("workflow")
		if wf != "" {
			id, err := resolveWorkflow(cmd, wf)
			if err != nil {
				return err
			}
			wf = id
		}
		branch, _ := f.GetString("branch")
		status, _ := f.GetString("status")
		event, _ := f.GetString("event")
		p, err := gp(cmd, "/runs"+q("workflow", wf, "branch", branch, "status", status, "event", event))
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
		t := newTable(cmd, "ID", "STATUS", "WORKFLOW", "BRANCH", "EVENT", "SHA", "ACTOR", "AGE")
		for _, r := range items {
			t.row(short8(sv(r, "id")), sv(r, "status"), sv(r, "workflow"), sv(r, "branch"), sv(r, "event"), shortSHA(sv(r, "sha")), sv(r, "actor"), age(sv(r, "created_at")))
		}
		return t.flush()
	})
	list.Aliases = []string{"ls"}
	list.Flags().String("workflow", "", "Workflow id, name or file")
	list.Flags().String("branch", "", "Branch")
	list.Flags().String("status", "", "queued, running, success, failure, cancelled, waiting_approval")
	list.Flags().String("event", "", "push, pull_request, workflow_dispatch, ...")
	addList(list)

	view := leaf("view ID", "Show a run and its jobs", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, id, err := runPath(cmd, args[0], "")
		if err != nil {
			return err
		}
		var run rec
		if _, err := apiDo("GET", p, nil, &run, nil); err != nil {
			return err
		}
		job, _ := cmd.Flags().GetString("job")
		if job != "" {
			j, err := pickJob(run, job)
			if err != nil {
				return err
			}
			if done, err := emit(cmd, j); done || err != nil {
				return err
			}
			for _, k := range []string{"id", "name", "status", "runner_labels", "started_at", "finished_at"} {
				if v := sv(j, k); v != "" {
					fmt.Printf("%-14s %s\n", k+":", v)
				}
			}
			return nil
		}
		if done, err := emit(cmd, run); done || err != nil {
			return err
		}
		fmt.Printf("Run %s  %s  %s\n", short8(id), sv(run, "status"), sv(run, "workflow"))
		fmt.Printf("  %s on %s  %s  by %s  %s\n", sv(run, "event"), sv(run, "branch"), shortSHA(sv(run, "sha")), sv(run, "actor"), age(sv(run, "created_at")))
		if pr := sv(run, "pr"); pr != "" {
			fmt.Printf("  pull request #%s\n", pr)
		}
		t := newTable(cmd, "JOB", "NAME", "STATUS", "RUNNER")
		for _, j := range asList(run, "jobs") {
			t.row(sv(j, "id"), sv(j, "name"), sv(j, "status"), sv(j, "runner_labels"))
		}
		if err := t.flush(); err != nil {
			return err
		}
		if u := sv(run, "html_url"); u != "" {
			fmt.Println(u)
		}
		return nil
	})
	view.Flags().String("job", "", "Show only this job (id or name)")

	logs := leaf("logs ID", "Print the log of a job (secrets are masked by the server)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		follow, _ := cmd.Flags().GetBool("follow")
		rp, id, err := runPath(cmd, args[0], "")
		if err != nil {
			return err
		}
		jobRef, _ := cmd.Flags().GetString("job")
		var run rec
		if _, err := apiDo("GET", rp, nil, &run, nil); err != nil {
			return err
		}
		j, err := pickJob(run, jobRef)
		if err != nil {
			return err
		}
		lp, _, _ := runPath(cmd, id, "/jobs/"+seg(sv(j, "id"))+"/logs")
		cursor := ""
		for {
			var chunk rec
			if _, err := apiDo("GET", lp+q("cursor", cursor), nil, &chunk, nil); err != nil {
				return err
			}
			if jsonMode(cmd) {
				if _, err := emit(cmd, chunk); err != nil {
					return err
				}
			} else {
				for _, l := range asAnyStrings(chunk["lines"]) {
					fmt.Println(l)
				}
			}
			next := sv(chunk, "next_cursor")
			if next != "" {
				cursor = next
			}
			if chunk["complete"] == true || !follow {
				return nil
			}
			time.Sleep(pollInterval)
		}
	})
	logs.Flags().String("job", "", "Job id or name (optional when the run has one job)")
	logs.Flags().Bool("follow", false, "Keep reading until the job completes")

	rerun := leaf("rerun ID", "Re-run a run (or only its failed jobs)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		failed, _ := cmd.Flags().GetBool("failed")
		p, id, err := runPath(cmd, args[0], "/rerun")
		if err != nil {
			return err
		}
		if failed {
			p += "?failed=true"
		}
		var out rec
		if _, err := apiDo("POST", p, rec{}, &out, idemHeaders(cmd, args)); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Run %s re-run requested.\n", short8(id))
		return nil
	})
	rerun.Flags().Bool("failed", false, "Re-run only the failed jobs")
	addIdem(rerun)

	cancel := leaf("cancel ID", "Cancel a running run", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, id, err := runPath(cmd, args[0], "/cancel")
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Cancel run "+short8(id)+"?"); err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", p, rec{}, &out, nil); err != nil {
			return err
		}
		fmt.Printf("Run %s cancelled.\n", short8(id))
		return nil
	})
	addYes(cancel)

	watch := leaf("watch ID", "Follow a run until it ends", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		es, _ := cmd.Flags().GetBool("exit-status")
		return watchRun(cmd, args[0], waitDuration(cmd), es)
	})
	watch.Flags().Bool("exit-status", false, "Exit 6 when the run does not succeed")
	addWait(watch, "the run (default 60m)")

	artifacts := leaf("artifacts ID", "List the artifacts of a run", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		p, _, err := runPath(cmd, args[0], "/artifacts")
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
		t := newTable(cmd, "NAME", "SIZE", "EXPIRES", "URL")
		for _, a := range items {
			t.row(sv(a, "name"), formatBytes(atoiSafe(sv(a, "size"))), sv(a, "expires_at"), sv(a, "download_url"))
		}
		return t.flush()
	})
	artifacts.Flags().BoolP("quiet", "q", false, "Print only names")

	g := group("runs", "Actions runs: list, view, logs, rerun, cancel, watch, artifacts", list, view, logs, rerun, cancel, watch, artifacts)
	g.Aliases = []string{"run"}
	rootCmd.AddCommand(g)
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
