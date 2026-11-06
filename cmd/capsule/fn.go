package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// capsule fn …: functions that run inside the project's tunnel on the work node (docs/vpn-connector.md).

func fnBase(cmd *cobra.Command) (string, error) {
	orgID, projectID, err := resolveOrgProject(cmd)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s/fn", orgID, projectID), nil
}

type fnFunction struct {
	Name           string   `json:"name"`
	Image          string   `json:"image"`
	Runtime        string   `json:"runtime"`
	Cron           string   `json:"cron"`
	MemoryMB       int      `json:"memory_mb"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxConcurrency int      `json:"max_concurrency"`
	Scopes         []string `json:"scopes"`
	Enabled        bool     `json:"enabled"`
}

type fnRun struct {
	ID          string  `json:"id"`
	Trigger     string  `json:"trigger"`
	TriggeredBy string  `json:"triggered_by"`
	Status      string  `json:"status"`
	QueuedAt    string  `json:"queued_at"`
	FinishedAt  *string `json:"finished_at"`
	ExitCode    *int    `json:"exit_code"`
	BytesIn     int64   `json:"bytes_in"`
	BytesOut    int64   `json:"bytes_out"`
	LogTail     string  `json:"log_tail"`
}

var fnCmd = &cobra.Command{Use: "fn", Short: "Functions that run inside the project's VPN tunnel"}

var fnDeployCmd = &cobra.Command{
	Use:   "deploy <name>",
	Short: "Define a function: image, tunnel, schedule, limits and the scopes its runs may use",
	Long: `Example:
  capsule fn deploy migrar --image registry/migrar:1 --vpn estudio --cron "0 2 * * *" --memory 512m --timeout 2h \
      --scope api:importacion --scope storage:biblioteca
With --runtime lambda the function runs as its own AWS Lambda: each invocation is capped at 15 minutes, so the function must honour
ctx.batch.remainingMs() and return {continue: cursor}; Capsule re-invokes it with the cursor and a new token until it is done.
Scopes are explicit (api:<name>, storage:<name>, db:<name>); there are no wildcards. Runs go one at a time unless --concurrency says otherwise.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		f := cmd.Flags()
		image, _ := f.GetString("image")
		entry, _ := f.GetString("entry")
		vpn, _ := f.GetString("vpn")
		cron, _ := f.GetString("cron")
		mem, _ := f.GetString("memory")
		timeout, _ := f.GetString("timeout")
		cpus, _ := f.GetFloat64("cpus")
		conc, _ := f.GetInt("concurrency")
		scopes, _ := f.GetStringSlice("scope")
		runtime, _ := f.GetString("runtime")
		body := map[string]any{"name": args[0], "image": image, "entry": entry, "runtime": runtime, "vpn": vpn, "cron": cron, "memory": mem,
			"timeout": timeout, "cpus": cpus, "max_concurrency": conc, "scopes": scopes}
		var resp fnFunction
		if err := apiClient.Post(base+"/functions", body, &resp); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(resp)
		}
		fmt.Printf("Function %s deployed (runtime %s, memory %dm, timeout %ds, concurrency %d, scopes %s)\n", resp.Name, resp.Runtime, resp.MemoryMB, resp.TimeoutSeconds, resp.MaxConcurrency, strings.Join(resp.Scopes, ","))
		return nil
	},
}

var fnListCmd = &cobra.Command{
	Use:   "list",
	Short: "List functions",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		var list []fnFunction
		if err := apiClient.Get(base+"/functions", &list); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tRUNTIME\tIMAGE\tCRON\tMEMORY\tTIMEOUT\tSCOPES")
		for _, x := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%dm\t%ds\t%s\n", x.Name, x.Runtime, x.Image, x.Cron, x.MemoryMB, x.TimeoutSeconds, strings.Join(x.Scopes, ","))
		}
		return tw.Flush()
	},
}

func fnTrigger(kind string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		var payload json.RawMessage
		if p, _ := cmd.Flags().GetString("payload"); p != "" {
			payload = json.RawMessage(p)
		}
		var run fnRun
		if err := apiClient.Post(fmt.Sprintf("%s/functions/%s/%s", base, args[0], kind), map[string]any{"payload": payload}, &run); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(run)
		}
		fmt.Printf("Run %s queued (%s)\n", run.ID, run.Trigger)
		return nil
	}
}

var fnRunCmd = &cobra.Command{Use: "run <name>", Short: "Run a function now (API trigger)", Args: cobra.ExactArgs(1), RunE: fnTrigger("run")}
var fnEnqueueCmd = &cobra.Command{Use: "enqueue <name>", Short: "Put a run in the function's queue (queue trigger)", Args: cobra.ExactArgs(1), RunE: fnTrigger("enqueue")}

var fnRunsCmd = &cobra.Command{
	Use:   "runs <name>",
	Short: "Run history: who, scopes, bytes",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		var runs []fnRun
		if err := apiClient.Get(fmt.Sprintf("%s/functions/%s/runs", base, args[0]), &runs); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(runs)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "RUN\tSTATUS\tTRIGGER\tBY\tQUEUED\tBYTES IN\tBYTES OUT")
		for _, r := range runs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%d\n", r.ID[:8], r.Status, r.Trigger, r.TriggeredBy, r.QueuedAt, r.BytesIn, r.BytesOut)
		}
		return tw.Flush()
	},
}

var fnLogsCmd = &cobra.Command{
	Use:   "logs <run-id>",
	Short: "Show a run and the tail of its log (secrets are masked)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		var r fnRun
		if err := apiClient.Get(base+"/runs/"+args[0], &r); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(r)
		}
		fmt.Printf("Run %s: %s (bytes in %d, out %d)\n%s", r.ID, r.Status, r.BytesIn, r.BytesOut, r.LogTail)
		return nil
	},
}

var fnCancelCmd = &cobra.Command{
	Use:   "cancel <run-id>",
	Short: "Cancel a queued or running run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		var r fnRun
		if err := apiClient.Post(base+"/runs/"+args[0]+"/cancel", nil, &r); err != nil {
			return err
		}
		fmt.Printf("Run %s: %s\n", r.ID, r.Status)
		return nil
	},
}

var fnDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a function",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		if err := apiClient.Delete(base + "/functions/" + args[0]); err != nil {
			return err
		}
		fmt.Println("Function deleted")
		return nil
	},
}

func init() {
	all := []*cobra.Command{fnDeployCmd, fnListCmd, fnRunCmd, fnEnqueueCmd, fnRunsCmd, fnLogsCmd, fnCancelCmd, fnDeleteCmd}
	for _, c := range all {
		c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	}
	f := fnDeployCmd.Flags()
	f.String("image", "", "container image of the function (built from @kyntocg/capsule-fn)")
	f.String("entry", "", "command inside the image (default: the image entrypoint)")
	f.String("runtime", "container", "where it runs: container (on the work node, no time cap) or lambda (its own AWS Lambda, resumable batches of 13 min)")
	f.String("vpn", "", "attach the project's tunnel by name")
	f.String("cron", "", "5-field cron expression (UTC)")
	f.String("memory", "512m", "memory limit (no swap)")
	f.String("timeout", "1h", "hard time limit per run (e.g. 90m, 2h)")
	f.Float64("cpus", 1, "CPU limit")
	f.Int("concurrency", 1, "runs at the same time (default one at a time)")
	f.StringSlice("scope", nil, "scope the run token carries: api:<name>, storage:<name>, db:<name> (repeatable)")
	_ = fnDeployCmd.MarkFlagRequired("image")
	fnRunCmd.Flags().String("payload", "", "JSON payload for the run")
	fnEnqueueCmd.Flags().String("payload", "", "JSON payload for the run")
	fnCmd.AddCommand(all...)
	rootCmd.AddCommand(fnCmd)
}
